package transcript

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
)

// DefaultFlushInterval is how often a Recorder ships what it holds.
const DefaultFlushInterval = 2 * time.Second

const (
	sendTimeout       = 10 * time.Second
	maxSendFailures   = 3
	maxPendingEntries = 1000
	maxTrackedTools   = 2048
	closeRetryDelay   = 250 * time.Millisecond
)

// ErrRefused tells a Recorder that ploegd will not take this Run's
// transcript, so it stops sending.
var ErrRefused = errors.New("transcript refused")

// Sender ships one batch of entries, in sequence order.
type Sender func(ctx context.Context, entries []Entry) error

// Options configure a Recorder.
type Options struct {
	// Thoughts records the agent's thought chunks.
	Thoughts bool
	// FlushInterval is DefaultFlushInterval when zero.
	FlushInterval time.Duration
	// Secrets returns the exact values to mask, read each time an entry is
	// recorded so a refreshed credential is masked too.
	Secrets func() []string
	Log     *slog.Logger
	Now     func() time.Time
}

// ToolEvent is one announcement or update of a tool call. Empty fields keep
// what earlier events said.
type ToolEvent struct {
	ID     string
	Title  string
	Kind   string
	Status string
	Input  string
	Paths  []string
}

type toolState struct {
	title, kind, status, input string
	paths                      []string
	output                     string
}

// Recorder turns agent activity into transcript entries and ships them in
// batches. Recording never blocks on the network and never fails: a Recorder
// that cannot ship drops entries and says so in the log. A nil Recorder
// records nothing.
type Recorder struct {
	send Sender
	opts Options
	log  *slog.Logger

	mu          sync.Mutex
	progress    Progress
	seq         int64
	pending     []Entry
	open        *Entry
	openTouched bool
	entries     int
	bytes       int
	truncated   bool
	tools       map[string]*toolState
	anonymous   int
	lastPlan    []Step
	failures    int
	disabled    bool
	dropped     int

	kick    chan struct{}
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// New starts a Recorder that ships through send.
func New(send Sender, opts Options) *Recorder {
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Recorder{
		send: send, opts: opts, log: log,
		tools: map[string]*toolState{},
		kick:  make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}),
	}
	go r.loop()
	return r
}

// Message records a chunk of the agent's reply. Consecutive chunks become one
// entry.
func (r *Recorder) Message(text string) { r.chunk(KindMessage, text) }

// Thought records a chunk of the agent's reasoning when Options.Thoughts is
// set. Consecutive chunks become one entry.
func (r *Recorder) Thought(text string) {
	if r == nil || !r.opts.Thoughts {
		return
	}
	r.chunk(KindThought, text)
}

// Stage records a step the worker or the adapter took.
func (r *Recorder) Stage(text string) {
	if r == nil || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeOpenLocked()
	r.appendLocked(Entry{Kind: KindStage, Text: text})
}

// Tool records a tool call when it is announced and each time its status,
// title or kind first becomes known or its status changes.
func (r *Recorder) Tool(ev ToolEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeOpenLocked()
	id := r.toolIDLocked(ev.ID)
	st, known := r.tools[id]
	if !known {
		st = &toolState{}
		if len(r.tools) < maxTrackedTools {
			r.tools[id] = st
		}
	}
	changed := !known
	set := func(dst *string, v string) {
		if v != "" && v != *dst {
			changed = changed || *dst == "" || dst == &st.status
			*dst = v
		}
	}
	set(&st.title, ev.Title)
	set(&st.kind, ev.Kind)
	set(&st.status, ev.Status)
	if ev.Input != "" && st.input == "" {
		st.input = ev.Input
		changed = true
	}
	for _, p := range ev.Paths {
		if p != "" && !slices.Contains(st.paths, p) {
			st.paths = append(st.paths, p)
		}
	}
	if st.status == "" {
		st.status = "pending"
	}
	if !changed {
		return
	}
	r.appendLocked(Entry{
		Kind: KindToolCall, ToolCallID: id, Title: st.title, ToolKind: st.kind,
		Status: st.status, Input: st.input, Paths: slices.Clone(st.paths),
	})
}

// ToolOutput records what a tool call returned. Output identical to the last
// output recorded for the same call is skipped.
func (r *Recorder) ToolOutput(id, text string) {
	if r == nil || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeOpenLocked()
	st, known := r.tools[id]
	if known {
		if st.output == text {
			return
		}
		st.output = text
	}
	r.appendLocked(Entry{Kind: KindToolOutput, ToolCallID: id, Text: text})
}

// Plan records the agent's plan when it differs from the last one.
func (r *Recorder) Plan(steps []Step) {
	if r == nil || len(steps) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Equal(steps, r.lastPlan) {
		return
	}
	r.lastPlan = slices.Clone(steps)
	r.closeOpenLocked()
	r.appendLocked(Entry{Kind: KindPlan, Plan: slices.Clone(steps)})
}

// Close records what is still open, ships everything it can before ctx ends,
// and stops the Recorder. It is safe to call more than once.
func (r *Recorder) Close(ctx context.Context) {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.done) })
	<-r.stopped
	r.mu.Lock()
	r.closeOpenLocked()
	r.mu.Unlock()
	for ctx.Err() == nil {
		if r.flush(ctx) {
			continue
		}
		r.mu.Lock()
		idle := r.disabled || len(r.pending) == 0
		r.mu.Unlock()
		if idle {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(closeRetryDelay):
		}
	}
	r.mu.Lock()
	left, dropped := len(r.pending), r.dropped
	r.pending, r.dropped = nil, 0
	r.mu.Unlock()
	if left+dropped > 0 {
		r.log.Warn("transcript entries were not delivered", "dropped", left+dropped)
	}
}

func (r *Recorder) chunk(kind Kind, text string) {
	if r == nil || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open != nil && r.open.Kind != kind {
		r.closeOpenLocked()
	}
	if r.open == nil {
		r.open = &Entry{Kind: kind, At: r.opts.Now()}
	}
	r.openTouched = true
	if r.open.Clipped {
		return
	}
	r.open.Text += text
	if len(r.open.Text) > MaxTextBytes {
		r.open.Text, _ = clip(r.open.Text, MaxTextBytes)
		r.open.Clipped = true
	}
}

func (r *Recorder) closeOpenLocked() {
	if r.open == nil {
		return
	}
	e := *r.open
	r.open, r.openTouched = nil, false
	r.appendLocked(e)
}

func (r *Recorder) toolIDLocked(id string) string {
	if id != "" {
		return id
	}
	r.anonymous++
	return "anon-" + strconv.Itoa(r.anonymous)
}

func (r *Recorder) appendLocked(e Entry) {
	if e.At.IsZero() {
		e.At = r.opts.Now()
	}
	var secrets []string
	if r.opts.Secrets != nil {
		secrets = r.opts.Secrets()
	}
	e = Clamp(NewRedactor(secrets...).Entry(e))
	r.noteProgressLocked(e)
	if r.truncated || r.disabled {
		return
	}
	e.Seq = r.seq + 1
	size := Size(e)
	if !Fits(r.entries, r.bytes, size) {
		r.truncated = true
		e = Marker(e.Seq, e.At)
		size = Size(e)
	}
	r.seq = e.Seq
	r.entries++
	r.bytes += size
	if len(r.pending) >= maxPendingEntries {
		r.pending = r.pending[1:]
		r.dropped++
	}
	r.pending = append(r.pending, e)
	if len(r.pending) >= MaxBatchEntries {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

// Progress is what a Run had got to: its latest plan and its last few tool
// calls, redacted like the transcript. The worker writes it into the note it
// leaves when a Run stops, so the next Run starts where this one ended.
type Progress struct {
	Plan   []Step
	Recent []Entry
}

// maxRecentTools is how many of the latest tool calls Progress keeps.
const maxRecentTools = 5

func (r *Recorder) noteProgressLocked(e Entry) {
	switch e.Kind {
	case KindPlan:
		r.progress.Plan = slices.Clone(e.Plan)
	case KindToolCall:
		for i, prior := range r.progress.Recent {
			if prior.ToolCallID == e.ToolCallID {
				r.progress.Recent = slices.Delete(r.progress.Recent, i, i+1)
				break
			}
		}
		r.progress.Recent = append(r.progress.Recent, e)
		if len(r.progress.Recent) > maxRecentTools {
			r.progress.Recent = r.progress.Recent[len(r.progress.Recent)-maxRecentTools:]
		}
	}
}

// Progress returns the Run's latest plan and last tool calls. A nil Recorder
// has none.
func (r *Recorder) Progress() Progress {
	if r == nil {
		return Progress{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return Progress{Plan: slices.Clone(r.progress.Plan), Recent: slices.Clone(r.progress.Recent)}
}

func (r *Recorder) loop() {
	defer close(r.stopped)
	tick := time.NewTicker(r.opts.FlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-tick.C:
			r.mu.Lock()
			if r.open != nil && !r.openTouched {
				r.closeOpenLocked()
			}
			r.openTouched = false
			r.mu.Unlock()
		case <-r.kick:
		}
		for r.flush(context.Background()) {
			select {
			case <-r.done:
				return
			default:
			}
		}
	}
}

// flush ships one batch and reports whether another batch is ready.
func (r *Recorder) flush(parent context.Context) bool {
	r.mu.Lock()
	if r.disabled || len(r.pending) == 0 || r.send == nil {
		r.mu.Unlock()
		return false
	}
	batch := slices.Clone(r.pending[:min(len(r.pending), MaxBatchEntries)])
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(parent, sendTimeout)
	err := r.send(ctx, batch)
	cancel()

	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.failures = 0
		r.removeThroughLocked(batch[len(batch)-1].Seq)
		return len(r.pending) > 0
	case errors.Is(err, ErrRefused):
		r.disabled = true
		r.dropped += len(r.pending)
		r.pending = nil
		r.log.Warn("ploegd refused the Run's transcript; recording stops", "err", err)
		return false
	default:
		r.failures++
		if r.failures < maxSendFailures {
			r.log.Warn("transcript batch not delivered; retrying", "entries", len(batch), "err", err)
			return false
		}
		r.failures = 0
		r.dropped += len(batch)
		r.removeThroughLocked(batch[len(batch)-1].Seq)
		r.log.Warn("transcript batch dropped after repeated failures", "entries", len(batch), "err", err)
		return false
	}
}

func (r *Recorder) removeThroughLocked(seq int64) {
	i := 0
	for i < len(r.pending) && r.pending[i].Seq <= seq {
		i++
	}
	r.pending = r.pending[i:]
}

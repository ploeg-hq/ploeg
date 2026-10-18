package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// notesFileEnv names the file an agent appends its findings to, one JSON
// learning per line, while it works (ADR-0088, amended 2026-10-17).
const notesFileEnv = "PLOEG_NOTES_FILE"

// liveNotesInterval is how often the worker reads the notes file, and
// liveNotesFileLimit how much of it the worker reads at all.
const (
	liveNotesInterval  = 5 * time.Second
	liveNotesFileLimit = 256 << 10
	liveNotesTimeout   = 10 * time.Second
)

// noteSender is the part of the Run API that takes notes during a Run.
type noteSender interface {
	Notes(ctx context.Context, runToken string, notes []harness.RunNote) error
}

// liveNotes reads the agent's notes file as it grows and sends each complete
// line to ploegd at once, so a Run that is stopped later keeps what it
// found. It sends at most maxLearnings notes, never one that carries one of
// the Run's credentials, and gives up quietly when ploegd takes none.
type liveNotes struct {
	path  string
	api   noteSender
	token string
	leaks leakScope
	log   *slog.Logger

	mu      sync.Mutex
	read    int64
	lines   int
	kept    int
	pending []harness.RunNote
	off     bool

	stop chan struct{}
	done chan struct{}
}

// startLiveNotes creates the empty notes file and starts reading it.
func startLiveNotes(path string, api noteSender, token string, leaks leakScope, log *slog.Logger) (*liveNotes, error) {
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		return nil, err
	}
	n := &liveNotes{path: path, api: api, token: token, leaks: leaks, log: log, stop: make(chan struct{}), done: make(chan struct{})}
	go n.loop()
	return n, nil
}

func (n *liveNotes) loop() {
	defer close(n.done)
	tick := time.NewTicker(liveNotesInterval)
	defer tick.Stop()
	for {
		select {
		case <-n.stop:
			return
		case <-tick.C:
			n.poll(context.Background())
		}
	}
}

// Close stops reading, reads the file once more and sends what is left. It
// is safe on a nil liveNotes and safe to call more than once.
func (n *liveNotes) Close(ctx context.Context) {
	if n == nil {
		return
	}
	n.mu.Lock()
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
	n.mu.Unlock()
	<-n.done
	n.poll(ctx)
}

// Sent is how many of the agent's notes reached ploegd or wait to.
func (n *liveNotes) Sent() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.kept
}

func (n *liveNotes) poll(ctx context.Context) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.off {
		return
	}
	n.readLocked()
	n.sendLocked(ctx)
}

func (n *liveNotes) readLocked() {
	f, err := os.Open(n.path)
	if err != nil {
		return
	}
	defer f.Close()
	if n.read >= liveNotesFileLimit {
		return
	}
	if _, err := f.Seek(n.read, 0); err != nil {
		return
	}
	buf := make([]byte, liveNotesFileLimit-n.read)
	got, _ := f.Read(buf)
	buf = buf[:got]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return
	}
	n.read += int64(end + 1)
	for _, line := range bytes.Split(buf[:end], []byte("\n")) {
		n.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 || n.kept >= maxLearnings {
			continue
		}
		var l harness.Learning
		if err := json.Unmarshal(line, &l); err != nil || strings.TrimSpace(l.Type) == "" || strings.TrimSpace(l.Title) == "" || strings.TrimSpace(l.Body) == "" {
			n.log.Warn("notes file line skipped: not a learning with a type, title and body", "line", n.lines)
			continue
		}
		if kind, found := n.leaks.watch.find(learningsText([]harness.Learning{l})); found {
			n.log.Warn("notes file line withheld: it carried one of the Run's credentials", "line", n.lines, "credential", string(kind))
			continue
		}
		n.kept++
		n.pending = append(n.pending, harness.RunNote{Key: strconv.Itoa(n.lines), Learning: l})
	}
}

func (n *liveNotes) sendLocked(ctx context.Context) {
	if len(n.pending) == 0 {
		return
	}
	if _, leaked := n.leaks.watch.leak(); leaked {
		n.off, n.pending = true, nil
		return
	}
	ctx, cancel := context.WithTimeout(ctx, liveNotesTimeout)
	defer cancel()
	err := n.api.Notes(ctx, n.token, n.pending)
	switch {
	case errors.Is(err, errNotesOff):
		n.log.Info("ploegd takes no notes during a Run; the outcome carries them instead")
		n.off = true
	case err != nil:
		n.log.Warn("notes not sent yet; the next read retries", "notes", len(n.pending), "err", err)
	default:
		n.log.Info("sent the Run's notes", "notes", len(n.pending))
		n.pending = nil
	}
}

// stopNoteFor reports whether the worker leaves a note on why the Run
// stopped: the Run ended without delivering, and not because it leaked.
func stopNoteFor(final harness.OutcomeReport) bool {
	if final.FailureReason == string(work.FailureCredentialLeak) {
		return false
	}
	return final.Outcome == work.OutcomeFailed || final.Outcome == work.OutcomeStuck
}

// stopNote is the worker's own note on why a Run stopped and how far it got,
// built only from what the worker saw, so the next Run starts where this one
// ended. No model writes it.
func stopNote(final harness.OutcomeReport, progress transcript.Progress, elapsed time.Duration, branch string, pushed bool) harness.Learning {
	summary := strings.TrimSpace(final.Summary)
	if summary == "" {
		summary = string(final.Outcome)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "An earlier Run of this work stopped after %s: %s.\n\n", roundElapsed(elapsed), summary)
	if reason := firstLine(final.StuckReason); reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n\n", reason)
	}
	if len(progress.Plan) > 0 {
		b.WriteString("Its plan when it stopped:\n\n")
		for _, step := range progress.Plan {
			mark := "[ ]"
			switch step.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			fmt.Fprintf(&b, "- %s %s\n", mark, clip(step.Content, 200))
		}
		b.WriteString("\n")
	}
	if len(progress.Recent) > 0 {
		b.WriteString("Its last commands, newest last:\n\n")
		for _, e := range progress.Recent {
			what := e.Title
			if what == "" {
				what = e.Input
			}
			fmt.Fprintf(&b, "- %s (%s)\n", clip(what, 200), e.Status)
		}
		b.WriteString("\n")
	}
	if pushed {
		fmt.Fprintf(&b, "Its pull request is on branch %s; start from there.\n", branch)
	} else {
		fmt.Fprintf(&b, "It opened no pull request. Check branch %s for anything it pushed before redoing its work.\n", branch)
	}
	return harness.Learning{
		Type:        "Pitfall",
		Title:       clip("An earlier Run stopped: "+summary, 120),
		Description: clip("What the previous Run had done when it stopped, recorded by Ploeg", 300),
		Tags:        []string{"run-stopped"},
		Body:        clip(b.String(), 4000),
		Subject:     stopNoteSubject,
	}
}

// stopNoteSubject makes each stop note replace the previous one (ADR-0122).
const stopNoteSubject = "run-stopped"

func roundElapsed(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "|\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return clip(s, 300)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// sendStopNote leaves the worker's note on why the Run stopped. It never
// changes the outcome: a note that cannot be sent is logged and dropped.
func sendStopNote(ctx context.Context, api noteSender, token string, leaks leakScope, log *slog.Logger, note harness.Learning) {
	if _, leaked := leaks.watch.leak(); leaked {
		return
	}
	if kind, found := leaks.watch.find(learningsText([]harness.Learning{note})); found {
		log.Warn("stop note withheld: it carried one of the Run's credentials", "credential", string(kind))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, liveNotesTimeout)
	defer cancel()
	switch err := api.Notes(ctx, token, []harness.RunNote{{Key: "stop", ByWorker: true, Learning: note}}); {
	case errors.Is(err, errNotesOff):
	case err != nil:
		log.Warn("stop note not sent", "err", err)
	default:
		log.Info("left a note on why the Run stopped")
	}
}

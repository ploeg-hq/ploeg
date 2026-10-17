package transcript

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu      sync.Mutex
	entries []Entry
	batches int
	fail    int
	err     error
}

func (s *sink) send(_ context.Context, entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.fail > 0 {
		s.fail--
		return errors.New("ploegd unreachable")
	}
	if len(entries) > MaxBatchEntries {
		return fmt.Errorf("batch of %d entries", len(entries))
	}
	s.batches++
	s.entries = append(s.entries, entries...)
	return nil
}

func (s *sink) got() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

func recorder(s *sink, opts Options) *Recorder {
	if opts.FlushInterval == 0 {
		opts.FlushInterval = time.Hour
	}
	return New(s.send, opts)
}

func closeNow(r *Recorder) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.Close(ctx)
}

func drain(r *Recorder) {
	for {
		r.mu.Lock()
		n := len(r.pending)
		r.mu.Unlock()
		if n < maxPendingEntries/2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func kinds(entries []Entry) string {
	var k []string
	for _, e := range entries {
		k = append(k, string(e.Kind))
	}
	return strings.Join(k, ",")
}

func TestChunksCoalesceIntoOneEntryPerKind(t *testing.T) {
	s := &sink{}
	r := recorder(s, Options{Thoughts: true})
	r.Thought("Let me ")
	r.Thought("look.")
	r.Message("I will ")
	r.Message("edit main.go.")
	r.Tool(ToolEvent{ID: "t1", Title: "Edit main.go", Kind: "edit"})
	r.Message("Done.")
	closeNow(r)

	got := s.got()
	if kinds(got) != "thought,message,tool_call,message" {
		t.Fatalf("kinds = %s", kinds(got))
	}
	if got[0].Text != "Let me look." || got[1].Text != "I will edit main.go." || got[3].Text != "Done." {
		t.Fatalf("coalesced text = %q, %q, %q", got[0].Text, got[1].Text, got[3].Text)
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d; want consecutive from 1", i, e.Seq)
		}
		if e.At.IsZero() {
			t.Fatalf("entry %d has no time", i)
		}
	}
}

func TestThoughtsAreLeftOutUnlessEnabled(t *testing.T) {
	s := &sink{}
	r := recorder(s, Options{})
	r.Thought("private reasoning")
	r.Message("reply")
	closeNow(r)
	if got := s.got(); kinds(got) != "message" {
		t.Fatalf("kinds = %s", kinds(got))
	}
}

func TestAQuietOpenMessageClosesAtTheNextTick(t *testing.T) {
	s := &sink{}
	r := New(s.send, Options{FlushInterval: 20 * time.Millisecond})
	r.Message("streamed")
	deadline := time.Now().Add(2 * time.Second)
	for len(s.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	closeNow(r)
	if got := s.got(); len(got) != 1 || got[0].Text != "streamed" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestToolCallsRecordAnnouncementAndStatusChangesOnly(t *testing.T) {
	s := &sink{}
	r := recorder(s, Options{})
	r.Tool(ToolEvent{ID: "t1", Title: "Run go test", Kind: "execute", Input: "go test ./..."})
	r.Tool(ToolEvent{ID: "t1", Status: "in_progress"})
	r.Tool(ToolEvent{ID: "t1", Status: "in_progress"})
	r.ToolOutput("t1", "ok  pkg")
	r.ToolOutput("t1", "ok  pkg")
	r.Tool(ToolEvent{ID: "t1", Status: "completed"})
	r.Plan([]Step{{Content: "test", Status: "completed"}})
	r.Plan([]Step{{Content: "test", Status: "completed"}})
	closeNow(r)

	got := s.got()
	if kinds(got) != "tool_call,tool_call,tool_output,tool_call,plan" {
		t.Fatalf("kinds = %s", kinds(got))
	}
	want := []string{"pending", "in_progress", "", "completed"}
	for i, status := range want {
		if got[i].Status != status {
			t.Fatalf("entry %d status = %q, want %q", i, got[i].Status, status)
		}
	}
	if got[3].Title != "Run go test" || got[3].Input != "go test ./..." || got[3].ToolKind != "execute" {
		t.Fatalf("a status change must carry what the call is: %+v", got[3])
	}
}

func TestEntriesAreRedactedBeforeTheyLeaveTheWorker(t *testing.T) {
	s := &sink{}
	runKey := "sk-live-abcdefghijklmnop"
	forgeToken := "forge0123456789token"
	r := recorder(s, Options{Secrets: func() []string { return []string{forgeToken, "short"} }})
	r.Message("key " + runKey[:10])
	r.Message(runKey[10:] + " and token ")
	r.Message(forgeToken + " and a short word")
	r.ToolOutput("t1", "Authorization: Bearer abcdefghijklmnop\npassword=hunter2hunter2\nhttps://user:pa55word@forge.example/repo.git\n"+
		"ghp_abcdefghijklmnopqrstuvwxyz0123\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----")
	r.Tool(ToolEvent{ID: "t2", Title: "curl -H 'x-api-key: " + strings.Repeat("a1", 12) + "'"})
	closeNow(r)

	all := ""
	for _, e := range s.got() {
		all += e.Text + e.Title + "\n"
	}
	for _, secret := range []string{runKey, forgeToken, "abcdefghijklmnop", "hunter2hunter2", "pa55word", "ghp_", "MIIE", strings.Repeat("a1", 12)} {
		if strings.Contains(all, secret) {
			t.Fatalf("transcript carries %q:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, "short word") || !strings.Contains(all, "forge.example/repo.git") {
		t.Fatalf("redaction removed ordinary text:\n%s", all)
	}
}

func TestOversizedTextIsClippedAtTheEntryLimit(t *testing.T) {
	s := &sink{}
	r := recorder(s, Options{})
	for range 10 {
		r.Message(strings.Repeat("é", 1000))
	}
	r.ToolOutput("t1", strings.Repeat("x", 3*MaxTextBytes))
	closeNow(r)
	got := s.got()
	if len(got) != 2 {
		t.Fatalf("entries = %d", len(got))
	}
	for _, e := range got {
		if len(e.Text) > MaxTextBytes || !e.Clipped {
			t.Fatalf("%s: %d bytes, clipped=%v", e.Kind, len(e.Text), e.Clipped)
		}
		if !strings.HasSuffix(got[0].Text, "é") {
			t.Fatal("clipping split a UTF-8 sequence")
		}
	}
}

func TestARunStopsRecordingAtItsLimitWithOneMarker(t *testing.T) {
	s := &sink{}
	r := recorder(s, Options{})
	for i := range MaxRunEntries + 50 {
		r.Stage(fmt.Sprintf("step %d", i))
		drain(r)
	}
	closeNow(r)
	got := s.got()
	if len(got) != MaxRunEntries {
		t.Fatalf("entries = %d, want %d", len(got), MaxRunEntries)
	}
	last := got[len(got)-1]
	if last.Kind != KindTruncated || last.Seq != MaxRunEntries {
		t.Fatalf("last entry = %+v", last)
	}
	for _, e := range got[:len(got)-1] {
		if e.Kind == KindTruncated {
			t.Fatal("more than one truncation marker")
		}
	}

	s = &sink{}
	r = recorder(s, Options{})
	for range 1000 {
		r.ToolOutput("", strings.Repeat("y", MaxTextBytes))
		drain(r)
	}
	closeNow(r)
	total := 0
	for _, e := range s.got() {
		total += Size(e)
	}
	if got := s.got(); got[len(got)-1].Kind != KindTruncated || total > MaxRunBytes {
		t.Fatalf("byte limit: %d bytes, last %s", total, got[len(got)-1].Kind)
	}
}

func TestARunsMarkerFitsWithinItsByteLimit(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s := &sink{}
	r := recorder(s, Options{Now: func() time.Time { return at }})
	recorded := MaxRunBytes - 1000
	r.mu.Lock()
	r.bytes = recorded
	r.mu.Unlock()
	for i := range 100 {
		r.Stage(fmt.Sprintf("step %d", i))
	}
	closeNow(r)
	got := s.got()
	total := recorded
	for _, e := range got {
		total += Size(e)
	}
	if len(got) < 2 || got[len(got)-1].Kind != KindTruncated || total > MaxRunBytes {
		t.Fatalf("recorded %d bytes ending in %s", total, kinds(got))
	}
}

func TestAnEntryFitsOnlyWhenTheMarkerStillFitsAfterIt(t *testing.T) {
	room := MaxRunBytes - largestMarkerBytes
	if !Fits(0, room-100, 100) || Fits(0, room-99, 100) {
		t.Fatal("the byte limit does not reserve room for the marker")
	}
	if !Fits(MaxRunEntries-2, 0, 1) || Fits(MaxRunEntries-1, 0, 1) {
		t.Fatal("the entry limit does not reserve a slot for the marker")
	}
	for _, at := range []time.Time{time.Now(), time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("", 5*3600+30*60))} {
		if size := Size(Marker(MaxSeq, at)); size > largestMarkerBytes {
			t.Fatalf("a marker of %d bytes exceeds the %d reserved", size, largestMarkerBytes)
		}
	}
}

func TestAFailedBatchIsRetriedAndOrderKept(t *testing.T) {
	s := &sink{fail: 2}
	r := recorder(s, Options{})
	for i := range 250 {
		r.Stage(fmt.Sprintf("step %d", i))
	}
	closeNow(r)
	got := s.got()
	if len(got) != 250 {
		t.Fatalf("entries = %d, want all 250 after two failures", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d seq %d", i, e.Seq)
		}
	}
}

func TestRefusalStopsRecording(t *testing.T) {
	s := &sink{err: fmt.Errorf("HTTP 404: %w", ErrRefused)}
	r := recorder(s, Options{})
	r.Stage("one")
	closeNow(r)
	r.Stage("after close")
	if len(s.got()) != 0 {
		t.Fatal("a refused transcript recorded entries")
	}
}

func TestANilRecorderRecordsNothing(t *testing.T) {
	var r *Recorder
	r.Message("x")
	r.Thought("x")
	r.Stage("x")
	r.Tool(ToolEvent{ID: "x"})
	r.ToolOutput("x", "x")
	r.Plan([]Step{{Content: "x"}})
	r.Close(context.Background())
}

func TestClampMapsUnknownEnumValues(t *testing.T) {
	e := Clamp(Entry{Kind: KindToolCall, ToolKind: "shell", Status: "running", Plan: []Step{{Content: "a", Status: "blocked"}}})
	if e.ToolKind != "other" || e.Status != "" || e.Plan[0].Status != "pending" {
		t.Fatalf("clamped = %+v", e)
	}
	e = Clamp(Entry{Kind: KindToolCall, ToolKind: "edit", Status: "failed"})
	if e.ToolKind != "edit" || e.Status != "failed" {
		t.Fatalf("known values changed: %+v", e)
	}
}

// Progress keeps the latest plan and the last tool calls, redacted, even
// after the transcript itself is full or no longer shipped.
func TestRecorder_ProgressKeepsThePlanAndLastToolsRedacted(t *testing.T) {
	r := New((&sink{}).send, Options{FlushInterval: time.Hour, Secrets: func() []string { return []string{"ghp_supersecret"} }})
	defer r.Close(context.Background())
	r.Plan([]Step{{Content: "Diagnose", Status: "completed"}, {Content: "Fix", Status: "in_progress"}})
	for i := range 7 {
		r.Tool(ToolEvent{ID: fmt.Sprint(i), Title: fmt.Sprintf("step %d", i), Status: "completed"})
	}
	r.Tool(ToolEvent{ID: "6", Status: "in_progress"})
	r.Tool(ToolEvent{ID: "x", Title: "push with ghp_supersecret", Status: "in_progress"})
	p := r.Progress()
	if len(p.Plan) != 2 || p.Plan[1].Status != "in_progress" {
		t.Errorf("plan %+v", p.Plan)
	}
	if len(p.Recent) != maxRecentTools {
		t.Fatalf("recent %d", len(p.Recent))
	}
	last := p.Recent[len(p.Recent)-1]
	if strings.Contains(last.Title, "ghp_supersecret") {
		t.Errorf("progress kept a secret: %q", last.Title)
	}
	if prev := p.Recent[len(p.Recent)-2]; prev.Title != "step 6" || prev.Status != "in_progress" {
		t.Errorf("an updated tool call did not move to the end: %+v", prev)
	}
	if (*Recorder)(nil).Progress().Plan != nil {
		t.Error("a nil Recorder has progress")
	}
}

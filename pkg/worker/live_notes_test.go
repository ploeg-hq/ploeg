package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeNoteSender struct {
	mu    sync.Mutex
	sent  []harness.RunNote
	err   error
	calls int
}

func (f *fakeNoteSender) Notes(_ context.Context, _ string, notes []harness.RunNote) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, notes...)
	return nil
}

func (f *fakeNoteSender) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, n := range f.sent {
		out = append(out, n.Key)
	}
	return out
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func testLeaks(secret string) leakScope {
	w := newLeakWatch()
	w.guard(credentialForgeToken, secret)
	return leakScope{run: "ploeg-t", workItem: "1", log: discardLog(), watch: w}
}

// Each complete line reaches ploegd as soon as the worker reads it, a half
// written line waits for its newline, and a line that is not a learning or
// that carries a credential is never sent.
func TestLiveNotes_SendEachLineAsTheAgentWritesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.jsonl")
	api := &fakeNoteSender{}
	n, err := startLiveNotes(path, api, "run-1", testLeaks("ghp_supersecret"), discardLog())
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, path, `{"type":"Pitfall","title":"Shutdown polls every 30 s","body":"Pass poll_interval=0.05."}`+"\n")
	appendLine(t, path, `{"type":"Fact","title":"half`)
	n.poll(context.Background())
	if got := api.keys(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("after the first line sent %v", got)
	}
	appendLine(t, path, ` a line","body":"now complete"}`+"\n")
	appendLine(t, path, "not json\n")
	appendLine(t, path, `{"type":"Fact","title":"token","body":"use ghp_supersecret"}`+"\n")
	n.Close(context.Background())
	if got := api.keys(); strings.Join(got, ",") != "1,2" {
		t.Errorf("sent %v, want lines 1 and 2 only", got)
	}
	if n.Sent() != 2 {
		t.Errorf("Sent() = %d", n.Sent())
	}
}

// A failed send is retried on the next read; a ploegd that takes no notes
// during a Run is not asked again.
func TestLiveNotes_RetriesAFailedSendAndStopsWhenPloegdTakesNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.jsonl")
	api := &fakeNoteSender{err: errors.New("ploegd restarting")}
	n, err := startLiveNotes(path, api, "run-1", testLeaks("s3cret-value"), discardLog())
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, path, `{"type":"Fact","title":"a","body":"b"}`+"\n")
	n.poll(context.Background())
	api.err = nil
	n.poll(context.Background())
	if got := api.keys(); len(got) != 1 {
		t.Fatalf("after a retry sent %v", got)
	}
	api.err = errNotesOff
	appendLine(t, path, `{"type":"Fact","title":"c","body":"d"}`+"\n")
	n.poll(context.Background())
	calls := api.calls
	appendLine(t, path, `{"type":"Fact","title":"e","body":"f"}`+"\n")
	n.Close(context.Background())
	if api.calls != calls {
		t.Errorf("asked ploegd again after it said it takes no notes")
	}
}

func TestLiveNotes_SendsAtMostTheLearningCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.jsonl")
	api := &fakeNoteSender{}
	n, err := startLiveNotes(path, api, "run-1", testLeaks("s3cret-value"), discardLog())
	if err != nil {
		t.Fatal(err)
	}
	for range maxLearnings + 3 {
		appendLine(t, path, `{"type":"Fact","title":"a","body":"b"}`+"\n")
	}
	n.Close(context.Background())
	if got := len(api.keys()); got != maxLearnings {
		t.Errorf("sent %d notes, want %d", got, maxLearnings)
	}
}

// The worker's stop note says where the Run got, from what the worker saw,
// and is left only when the Run ended without delivering.
func TestStopNote_SaysWhereTheStoppedRunGot(t *testing.T) {
	final := harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "acp idle watchdog stopped the agent: no protocol activity for 10m0s",
		StuckReason: "no protocol activity for 10m0s after 288 events | stderr: noise"}
	progress := transcript.Progress{
		Plan:   []transcript.Step{{Content: "Diagnose the slow job", Status: "completed"}, {Content: "Speed up the suites", Status: "in_progress"}, {Content: "Open the PR", Status: "pending"}},
		Recent: []transcript.Entry{{Kind: transcript.KindToolCall, Title: "Time distill mutation after fix", Status: "in_progress"}},
	}
	note := stopNote(final, progress, 45*time.Minute+20*time.Second, "agent/vik-2049", false)
	for _, want := range []string{"stopped after 45m", "idle watchdog", "[x] Diagnose the slow job", "[~] Speed up the suites", "[ ] Open the PR",
		"Time distill mutation after fix (in_progress)", "opened no pull request", "agent/vik-2049"} {
		if !strings.Contains(note.Body, want) {
			t.Errorf("stop note lacks %q:\n%s", want, note.Body)
		}
	}
	if strings.Contains(note.Body, "stderr") || note.Type != "Pitfall" || !strings.HasPrefix(note.Title, "An earlier Run stopped") {
		t.Errorf("note %+v", note)
	}
	for _, c := range []struct {
		report harness.OutcomeReport
		want   bool
	}{
		{harness.OutcomeReport{Outcome: work.OutcomeStuck}, true},
		{harness.OutcomeReport{Outcome: work.OutcomePROpened}, false},
		{harness.OutcomeReport{Outcome: work.OutcomeFailed, FailureReason: string(work.FailureCredentialLeak)}, false},
	} {
		if got := stopNoteFor(c.report); got != c.want {
			t.Errorf("stopNoteFor(%s, %s) = %v", c.report.Outcome, c.report.FailureReason, got)
		}
	}
}

func TestStopNote_SaysHowLongAShortRunWorked(t *testing.T) {
	final := harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "stopped on purpose"}
	for elapsed, want := range map[time.Duration]string{
		21*time.Second + 400*time.Millisecond: "stopped after 21s:",
		90 * time.Second:                      "stopped after 2m0s:",
		45*time.Minute + 20*time.Second:       "stopped after 45m0s:",
	} {
		if note := stopNote(final, transcript.Progress{}, elapsed, "agent/x", false); !strings.Contains(note.Body, want) {
			t.Errorf("after %s the stop note should say %q:\n%s", elapsed, want, note.Body)
		}
	}
}

// A stop note that would carry a credential is never sent.
func TestStopNote_WithheldWhenItCarriesACredential(t *testing.T) {
	api := &fakeNoteSender{}
	note := harness.Learning{Type: "Pitfall", Title: "t", Body: "last command: git push https://x:ghp_supersecret@forge"}
	sendStopNote(context.Background(), api, "run-1", testLeaks("ghp_supersecret"), discardLog(), note)
	if api.calls != 0 {
		t.Error("a stop note carrying a credential was sent")
	}
	sendStopNote(context.Background(), api, "run-1", testLeaks("ghp_supersecret"), discardLog(), harness.Learning{Type: "Pitfall", Title: "t", Body: "b"})
	if got := api.keys(); len(got) != 1 || got[0] != "stop" || !api.sent[0].ByWorker {
		t.Errorf("stop note sent as %+v", api.sent)
	}
}

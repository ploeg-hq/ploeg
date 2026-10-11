//go:build unix

package acp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

type transcriptSink struct {
	mu      sync.Mutex
	entries []transcript.Entry
}

func (s *transcriptSink) send(_ context.Context, entries []transcript.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	return nil
}

func (s *transcriptSink) recorder() *transcript.Recorder {
	return transcript.New(s.send, transcript.Options{Thoughts: true, FlushInterval: time.Hour})
}

func (s *transcriptSink) closed(r *transcript.Recorder) []transcript.Entry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.Close(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func update(u string) []byte {
	return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":` + u + `}}`)
}

func TestTranscribeTurnsSessionUpdatesIntoEntries(t *testing.T) {
	sink := &transcriptSink{}
	rec := sink.recorder()
	for _, u := range []string{
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"SECRET TICKET BODY"}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Read the "}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"handler first."}}`,
		`{"sessionUpdate":"plan","entries":[{"content":"read","status":"in_progress","priority":"high"},{"content":"fix","status":"pending"}]}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Run tests","kind":"execute","status":"pending","rawInput":{"command":["go","test","./..."]}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"failed","content":[{"type":"content","content":{"type":"text","text":"FAIL ./pkg"}}]}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t2","title":"Edit main.go","kind":"edit","status":"completed","locations":[{"path":"main.go"}],"content":[{"type":"diff","path":"main.go","oldText":"a\nb\nc\n","newText":"a\nB\nc\n"}]}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t3","title":"Read","kind":"read","status":"completed","rawOutput":{"output":"package main"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Fixed "}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"the handler."}}`,
		`{"sessionUpdate":"usage_update","used":10,"size":100}`,
	} {
		transcribe(rec, update(u))
	}
	transcribe(rec, []byte(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`))
	transcribe(rec, []byte(`not json`))
	got := sink.closed(rec)

	var kinds []string
	for _, e := range got {
		kinds = append(kinds, string(e.Kind))
	}
	want := "thought,plan,tool_call,tool_call,tool_call,tool_output,tool_call,tool_output,tool_call,tool_output,message"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("kinds = %s\nwant    %s", strings.Join(kinds, ","), want)
	}
	if got[0].Text != "Read the handler first." {
		t.Errorf("thought = %q", got[0].Text)
	}
	if len(got[1].Plan) != 2 || got[1].Plan[0] != (transcript.Step{Content: "read", Status: "in_progress"}) {
		t.Errorf("plan = %+v", got[1].Plan)
	}
	if got[2].Input != "go test ./..." || got[2].ToolKind != "execute" || got[2].Status != "pending" {
		t.Errorf("announcement = %+v", got[2])
	}
	if got[4].Status != "failed" || got[5].Text != "FAIL ./pkg" || got[5].ToolCallID != "t1" {
		t.Errorf("failure = %+v / %+v", got[4], got[5])
	}
	if got[7].Text != "edit main.go\n@@ line 2: -1 +1 @@\n-b\n+B" {
		t.Errorf("diff = %q", got[7].Text)
	}
	if len(got[6].Paths) != 1 || got[6].Paths[0] != "main.go" {
		t.Errorf("paths = %v", got[6].Paths)
	}
	if got[9].Text != "package main" {
		t.Errorf("raw output = %q", got[9].Text)
	}
	if got[10].Text != "Fixed the handler." {
		t.Errorf("message = %q", got[10].Text)
	}
	for _, e := range got {
		if strings.Contains(e.Text, "SECRET TICKET BODY") {
			t.Fatal("the prompt echo reached the transcript")
		}
	}
}

func TestRun_RecordsTheSessionInTheTranscript(t *testing.T) {
	bin := writeScript(t, fakeAgentScript(`"end_turn"`, `
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"read","status":"completed","title":"read go.mod"}}}'
      emit '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Looked around; nothing to change."}}}}'
`))
	a, err := New("custom", ProfileOverrides{Argv: []string{bin}}, Options{
		PromptTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, CancelGrace: time.Second, TermGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &transcriptSink{}
	env := testEnv(t)
	env.Transcript = sink.recorder()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.Run(ctx, testSpec(), env); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := sink.closed(env.Transcript)
	var lines []string
	for _, e := range got {
		lines = append(lines, string(e.Kind)+":"+e.Text+e.Title)
	}
	want := []string{"stage:Agent session started (custom)", "tool_call:read go.mod", "message:Looked around; nothing to change.", "stage:Agent turn ended: end_turn"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %q\nwant %q", lines, want)
	}
}

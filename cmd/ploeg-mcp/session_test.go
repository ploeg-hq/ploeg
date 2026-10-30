package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const previousProtocol = "2025-11-25"

// sessionClient connects on the protocol before 2026-07-28, as a client does
// when server/discover fails: it initializes, keeps the session id and
// declares caps once.
func sessionClient(t *testing.T, url, token string, caps *mcp.ClientCapabilities, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true, MaxRetries: -1}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Test Client", Version: "1.2.3"}, &mcp.ClientOptions{Capabilities: caps, ElicitationHandler: elicit}).
		Connect(context.Background(), transport, &mcp.ClientSessionOptions{ProtocolVersion: previousProtocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if got := cs.InitializeResult().ProtocolVersion; got != previousProtocol {
		t.Fatalf("negotiated %s, want %s", got, previousProtocol)
	}
	return cs
}

var formOnly = &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}

func TestAClientOnThePreviousProtocolConfirmsInItsSession(t *testing.T) {
	a := newActingRemote(t)
	yes := accept()
	cs := sessionClient(t, a.url, a.token([]string{"ploeg-stewards"}, "ploeg:read"), formOnly, yes.elicit)
	id := stuckItem(t, "session-retry-1")
	out, res := act(t, cs, "ploeg_retry_work", map[string]any{"ref": id, "note": "Try the smaller fix."})
	if res.IsError || !out.Done || out.State != "queued" || !strings.Contains(yes.last(), "Retry Work Item "+id) {
		t.Fatalf("retry: %s %+v, asked %q", textOf(res), out, yes.last())
	}
	got, n := lastAttribution(t, id, store.AuditWorkItemRequeued)
	if n != 1 {
		t.Fatalf("ploegd requeued %d times", n)
	}
	wantConfirmedBy(t, got, yes.last())
}

func TestADeclinedConfirmationInASessionChangesNothing(t *testing.T) {
	a := newActingRemote(t)
	for _, answer := range []*mcp.ElicitResult{{Action: "decline"}, {Action: "cancel"}, {Action: "accept", Content: map[string]any{"confirm": false}}} {
		asked := &confirmer{answer: answer}
		cs := sessionClient(t, a.url, a.token([]string{"ploeg-stewards"}, "ploeg:read"), formOnly, asked.elicit)
		id := stuckItem(t, "session-decline-"+answer.Action+fmt.Sprint(len(answer.Content)))
		out, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": id})
		want := notConfirmed
		if answer.Action == "cancel" {
			want = notAnswered
		}
		if res.IsError || out.Done || out.Summary != want || asked.last() == "" {
			t.Fatalf("%s: %s %+v", answer.Action, textOf(res), out)
		}
		if state := itemState(t, id); state != "needs_human" {
			t.Fatalf("%s: the Work Item became %s", answer.Action, state)
		}
	}
}

func TestAClientWithoutElicitationIsRefusedInItsSession(t *testing.T) {
	a := newActingRemote(t)
	cs := sessionClient(t, a.url, a.token([]string{"ploeg-stewards"}, "ploeg:read"), &mcp.ClientCapabilities{}, nil)
	id := stuckItem(t, "session-no-elicit")
	if _, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": id}); !res.IsError || !strings.Contains(textOf(res), "cannot ask you to confirm") {
		t.Fatalf("refusal: %s", textOf(res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("ploegd was called without a confirmation")
	}
}

func TestASessionAnswersOnlyThePersonAndGroupThatOpenedIt(t *testing.T) {
	a := newActingRemote(t)
	steward := a.token([]string{"ploeg-stewards"}, "ploeg:read")
	resp, body := a.post(steward, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"elicitation":{}},"clientInfo":{"name":"old","version":"1"}}}`)
	session := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || session == "" {
		t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	for name, token := range map[string]string{
		"another person":           a.tokenFor("subject-2", []string{"ploeg-stewards"}, "ploeg:read"),
		"the same person, a group": a.token([]string{"ploeg-readers"}, "ploeg:read"),
	} {
		resp, body := a.postIn(token, session, list)
		if resp.StatusCode != http.StatusForbidden || bytes.Contains(body, []byte("ploeg_stop_work")) {
			t.Fatalf("%s used the session: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body := a.postIn(steward, session, list); resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ploeg_stop_work")) {
		t.Fatalf("the person who opened it: %d %s", resp.StatusCode, body)
	}
}

func TestAPreviousProtocolClientConfirmsAtThePageInItsSession(t *testing.T) {
	s := newSigningRemote(t, nil)
	id := stuckItem(t, "session-page-1")
	b := newBrowser(t)
	var opened []string
	signed := make(chan struct{})
	openPage := func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		opened = append(opened, req.Params.URL)
		go func() {
			defer close(signed)
			time.Sleep(300 * time.Millisecond)
			if status, body := b.signIn(t, req.Params.URL); status != http.StatusOK {
				t.Errorf("signing in answered %d: %s", status, body)
			}
		}()
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}
	cs := sessionClient(t, s.url, s.token([]string{"ploeg-stewards"}, "ploeg:read"), caps, openPage)
	out, res := act(t, cs, "ploeg_retry_work", map[string]any{"ref": id})
	if len(opened) > 0 {
		<-signed
	}
	if res.IsError || !out.Done || len(opened) != 1 || itemState(t, id) != "queued" {
		t.Fatalf("retry: %s %+v, pages opened %d", textOf(res), out, len(opened))
	}
}

func TestAPreviousProtocolClientThatSkipsThePageNeverActs(t *testing.T) {
	s := newSigningRemote(t, map[string]string{"PLOEG_MCP_CONFIRMATION_TTL": "1m"})
	id := stuckItem(t, "session-page-skip")
	selfAnswering := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{confirmFieldName: true}}, nil
	}
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{}}}
	cs := sessionClient(t, s.url, s.token([]string{"ploeg-stewards"}, "ploeg:read"), caps, selfAnswering)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ploeg_retry_work", Arguments: map[string]any{"ref": id}}); err == nil && !res.IsError && done(t, res).Done {
		t.Fatalf("a client answering its own elicitations acted: %+v", done(t, res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a client answering its own elicitations acted")
	}

	form := sessionClient(t, s.url, s.token([]string{"ploeg-stewards"}, "ploeg:read"), formOnly, selfAnswering)
	if _, res := act(t, form, "ploeg_stop_work", map[string]any{"ref": id}); !res.IsError || !strings.Contains(textOf(res), "cannot open a confirmation page") {
		t.Fatalf("a form-only session client: %s", textOf(res))
	}
}

func (r *remote) postIn(token, session, body string) (*http.Response, []byte) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", previousProtocol)
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

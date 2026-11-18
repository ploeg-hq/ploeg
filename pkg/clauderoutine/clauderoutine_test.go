package clauderoutine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fixtureToken = "sk-ant-oat01-routine-trigger-fixture"

func fakeRoutines(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request, *[]string) {
	t.Helper()
	var seen []*http.Request
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, r)
		texts = append(texts, req.Text)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen, &texts
}

const fireOK = `{"type":"routine_fire","claude_code_session_id":"session_01HJKLMNOPQRSTUVWXYZ","claude_code_session_url":"https://claude.ai/code/session_01HJKLMNOPQRSTUVWXYZ"}`

func TestFireSendsTheTokenTheDatedBetaHeaderAndTheText(t *testing.T) {
	srv, seen, texts := fakeRoutines(t, 200, fireOK)
	c := &Client{BaseURL: srv.URL}
	got, err := c.Fire(context.Background(), srv.URL+"/v1/claude_code/routines/trig_01ABCDEFGHJK/fire", fixtureToken, "do the work")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "session_01HJKLMNOPQRSTUVWXYZ" || !strings.HasSuffix(got.URL, got.ID) {
		t.Fatalf("session = %+v", got)
	}
	r := (*seen)[0]
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixtureToken ||
		r.Header.Get("anthropic-beta") != Beta || r.Header.Get("anthropic-version") != APIVersion {
		t.Fatalf("request = %s %v", r.Method, r.Header)
	}
	if (*texts)[0] != "do the work" {
		t.Fatalf("text = %q", (*texts)[0])
	}
}

func TestFireNamesEachRefusalAndNeverCarriesTheToken(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{}`, ErrTokenRefused},
		{403, `{}`, ErrTokenRefused},
		{404, `{}`, ErrNotFound},
		{429, `{}`, ErrRateLimited},
		{500, `{}`, ErrUnavailable},
		{200, `not json`, ErrUnavailable},
		{200, `{"type":"something_else"}`, ErrUnavailable},
	} {
		srv, _, _ := fakeRoutines(t, tc.status, tc.body)
		_, err := (&Client{BaseURL: srv.URL}).Fire(context.Background(), srv.URL+"/v1/claude_code/routines/trig_01ABCDEFGHJK/fire", fixtureToken, "x")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d %s: err = %v, want %v", tc.status, tc.body, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), fixtureToken) {
			t.Errorf("status %d: the error carries the token", tc.status)
		}
		if Reason(err) != tc.want.Error() {
			t.Errorf("status %d: reason = %q", tc.status, Reason(err))
		}
	}
}

func TestAFireURLIsAcceptedOnlyAtTheDocumentedPathOnItsBase(t *testing.T) {
	ok := "https://api.anthropic.com/v1/claude_code/routines/trig_01ABCDEFGHJKLMNOPQRSTUVW/fire"
	if err := CheckFireURL("", ok); err != nil {
		t.Fatalf("documented fire URL refused: %v", err)
	}
	for _, bad := range []string{
		"http://api.anthropic.com/v1/claude_code/routines/trig_01ABCDEFGHJK/fire",
		"https://evil.example/v1/claude_code/routines/trig_01ABCDEFGHJK/fire",
		"https://api.anthropic.com.evil.example/v1/claude_code/routines/trig_01ABCDEFGHJK/fire",
		"https://user@api.anthropic.com/v1/claude_code/routines/trig_01ABCDEFGHJK/fire",
		"https://api.anthropic.com/v1/claude_code/routines/trig_01ABCDEFGHJK/fire?x=1",
		"https://api.anthropic.com/v1/claude_code/routines/trig_01ABCDEFGHJK/../../messages",
		"https://api.anthropic.com/v1/messages",
	} {
		if CheckFireURL("", bad) == nil {
			t.Errorf("fire URL %q accepted", bad)
		}
	}
	srv, seen, _ := fakeRoutines(t, 200, fireOK)
	if _, err := (&Client{BaseURL: "https://api.anthropic.com"}).Fire(context.Background(), srv.URL+"/v1/claude_code/routines/trig_01ABCDEFGHJK/fire", fixtureToken, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a fire URL off the base: err = %v", err)
	}
	if len(*seen) != 0 {
		t.Fatal("the token was sent to a host off the base")
	}
}

func TestTheFireTextCarriesTheWorkItemTheRunAndTheBranch(t *testing.T) {
	text := Text("VIK-12", "42", "agent/vik-12", "development", "Add a health check.")
	for _, want := range []string{"VIK-12", "Run 42", "agent/vik-12", "development", "Add a health check."} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

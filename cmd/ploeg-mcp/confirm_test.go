package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var sealedAt = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func sealedState() confirmState {
	return confirmState{Tool: "ploeg_stop_work", ID: "confirm-1", CommandID: "mcp-1", Effect: digest("Stop it."), Args: digest(`{"ref":"7"}`),
		Actor: signedInPerson, Expires: sealedAt.Add(confirmWindow).Unix()}
}

func confirmationsAt(key string, at *time.Time) *confirmations {
	return newConfirmations([]byte(key), func() time.Time { return *at })
}

func TestASealedConfirmationOpensUnchangedWithinItsWindow(t *testing.T) {
	now := sealedAt
	c := confirmationsAt(strings.Repeat("k", minConfirmationKeyBytes), &now)
	now = sealedAt.Add(confirmWindow)
	got, err := c.open(c.seal(sealedState()))
	if err != nil || got != sealedState() {
		t.Fatalf("open = %+v, %v; want %+v", got, err, sealedState())
	}
}

func TestAConfirmationSealedWithAnotherKeyIsRefused(t *testing.T) {
	now := sealedAt
	ours, theirs := confirmationsAt(strings.Repeat("k", 32), &now), confirmationsAt(strings.Repeat("x", 32), &now)
	if _, err := ours.open(theirs.seal(sealedState())); !errors.Is(err, errForeignConfirmation) {
		t.Fatalf("open = %v, want errForeignConfirmation", err)
	}
}

func TestAnAlteredOrUnsignedConfirmationIsRefused(t *testing.T) {
	now := sealedAt
	c := confirmationsAt(strings.Repeat("k", 32), &now)
	sealed := c.seal(sealedState())
	payload, tag, _ := strings.Cut(sealed, ".")
	raw, _ := json.Marshal(sealedState())
	unsigned := base64.RawURLEncoding.EncodeToString(raw)
	altered := sealedState()
	altered.Args = digest(`{"ref":"8"}`)
	alteredRaw, _ := json.Marshal(altered)
	otherActor := sealedState()
	otherActor.Actor = "oidc-00000000000000000000000000000000"
	otherActorRaw, _ := json.Marshal(otherActor)
	for name, state := range map[string]string{
		"the pre-signing unsigned form": unsigned,
		"no tag":                        payload,
		"an empty tag":                  payload + ".",
		"a truncated tag":               payload + "." + tag[:len(tag)-2],
		"altered arguments":             base64.RawURLEncoding.EncodeToString(alteredRaw) + "." + tag,
		"another actor":                 base64.RawURLEncoding.EncodeToString(otherActorRaw) + "." + tag,
		"empty":                         "",
	} {
		if _, err := c.open(state); !errors.Is(err, errForeignConfirmation) {
			t.Errorf("%s: open = %v, want errForeignConfirmation", name, err)
		}
	}
}

func TestAnExpiredConfirmationIsRefused(t *testing.T) {
	now := sealedAt
	c := confirmationsAt(strings.Repeat("k", 32), &now)
	sealed := c.seal(sealedState())
	now = sealedAt.Add(confirmWindow + time.Second)
	if _, err := c.open(sealed); !errors.Is(err, errExpiredConfirmation) {
		t.Fatalf("open = %v, want errExpiredConfirmation", err)
	}
}

func TestAConfirmationIsSpentOnce(t *testing.T) {
	now := sealedAt
	c := confirmationsAt(strings.Repeat("k", 32), &now)
	if err := c.spend(sealedState()); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if err := c.spend(sealedState()); !errors.Is(err, errSpentConfirmation) {
		t.Fatalf("second spend = %v, want errSpentConfirmation", err)
	}
	other := sealedState()
	other.ID = "confirm-2"
	if err := c.spend(other); err != nil {
		t.Fatalf("another confirmation: %v", err)
	}
}

func TestExecuteGroupsLoadWithAConfirmationKey(t *testing.T) {
	env := map[string]string{
		"PLOEG_URL":            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":     "https://id.example/application/o/ploeg-mcp/",
		"PLOEG_MCP_CLIENT_IDS": "ploeg-mcp",
		"PLOEG_MCP_GROUPS":     `[{"group":"g","tokenEnv":"READERS_TOKEN","execute":true}]`,
		"READERS_TOKEN":        strings.Repeat("a", 64),
		confirmationKeyEnv:     strings.Repeat("k", minConfirmationKeyBytes),
	}
	cfg, err := loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil || cfg.confirmations == nil {
		t.Fatalf("an execute group with a key: %v", err)
	}
}

type issuedStates struct {
	mu     sync.Mutex
	states []string
}

func (s *issuedStates) last(t *testing.T) (string, string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.states) == 0 {
		t.Fatal("ploeg-mcp issued no confirmation state")
	}
	sealed := s.states[len(s.states)-1]
	payload, _, _ := strings.Cut(sealed, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	var state confirmState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return sealed, state.ID
}

type recording struct {
	next   http.RoundTripper
	issued *issuedStates
}

func (r recording) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var msg struct {
		Result struct {
			RequestState string `json:"requestState"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &msg) == nil && msg.Result.RequestState != "" {
		r.issued.mu.Lock()
		r.issued.states = append(r.issued.states, msg.Result.RequestState)
		r.issued.mu.Unlock()
	}
	return resp, nil
}

// unanswered connects as subject with a client that records each
// confirmation state ploeg-mcp issues and never answers the person's
// confirmation, so a test can replay what a client holds.
func (a acting) unanswered(t *testing.T, subject string, issued *issuedStates) *mcp.ClientSession {
	t.Helper()
	never := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return nil, errors.New("the person has not answered")
	}
	transport := &mcp.StreamableClientTransport{Endpoint: a.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: recording{next: bearer{a.tokenFor(subject, []string{"ploeg-stewards"}, "ploeg:read")}, issued: issued}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Test Client", Version: "1.2.3"}, &mcp.ClientOptions{ElicitationHandler: never}).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func answered(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, sealed, id string) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, RequestState: sealed,
		InputResponses: mcp.InputResponseMap{id: &mcp.ElicitResult{Action: "accept", Content: map[string]any{confirmFieldName: true}}}})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestAClientCannotForgeTheConfirmationInOneCall(t *testing.T) {
	a := newActingRemote(t)
	cs := a.unanswered(t, "subject-1", &issuedStates{})
	id := stuckItem(t, "mcp-forged-1")
	args := map[string]any{"ref": id}
	argsJSON, _ := json.Marshal(refIn{Ref: id})
	forged := confirmState{Tool: "ploeg_stop_work", ID: "confirm-forged", CommandID: "mcp-forged", Effect: digest("anything"), Args: digest(string(argsJSON)),
		Actor: signedInPerson, Expires: time.Now().Add(time.Minute).Unix()}
	raw, _ := json.Marshal(forged)
	for name, state := range map[string]string{
		"the unsigned form":           base64.RawURLEncoding.EncodeToString(raw),
		"a state under a guessed key": newConfirmations([]byte(strings.Repeat("g", 32)), time.Now).seal(forged),
	} {
		res := answered(t, cs, "ploeg_stop_work", args, state, forged.ID)
		if !res.IsError || !strings.Contains(textOf(res), "does not belong") {
			t.Fatalf("%s: %s", name, textOf(res))
		}
	}
	if state := itemState(t, id); state != "needs_human" {
		t.Fatalf("a forged confirmation made the Work Item %s", state)
	}
}

func TestAnIssuedConfirmationActsOnceForItsActionArgumentsAndPerson(t *testing.T) {
	a := newActingRemote(t)
	issued := &issuedStates{}
	cs := a.unanswered(t, "subject-1", issued)
	target, other := stuckItem(t, "mcp-bound-1"), stuckItem(t, "mcp-bound-2")
	args := map[string]any{"ref": target}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_retry_work", Arguments: args}); err == nil {
		t.Fatal("an unanswered confirmation went ahead")
	}
	sealed, confirmation := issued.last(t)

	for name, attempt := range map[string]struct {
		cs   *mcp.ClientSession
		tool string
		args map[string]any
	}{
		"altered arguments": {cs, "ploeg_retry_work", map[string]any{"ref": other}},
		"another action":    {cs, "ploeg_stop_work", args},
		"another person":    {a.unanswered(t, "subject-2", &issuedStates{}), "ploeg_retry_work", args},
	} {
		res := answered(t, attempt.cs, attempt.tool, attempt.args, sealed, confirmation)
		if !res.IsError || !strings.Contains(textOf(res), "does not belong") {
			t.Fatalf("%s: %s", name, textOf(res))
		}
	}
	if itemState(t, target) != "needs_human" || itemState(t, other) != "needs_human" {
		t.Fatal("a misbound confirmation changed a Work Item")
	}

	res := answered(t, cs, "ploeg_retry_work", args, sealed, confirmation)
	var out actionOut
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	if res.IsError || !out.Done || itemState(t, target) != "queued" {
		t.Fatalf("the person's own confirmation: %s %+v", textOf(res), out)
	}

	res = answered(t, cs, "ploeg_retry_work", args, sealed, confirmation)
	if !res.IsError || !strings.Contains(textOf(res), "already used") {
		t.Fatalf("a replayed confirmation: %s", textOf(res))
	}
	if _, n := lastAttribution(t, target, store.AuditWorkItemRequeued); n != 1 {
		t.Fatalf("requeued %d times", n)
	}
}

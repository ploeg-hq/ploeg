package githubissues

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const testSecret = "fixture-secret"

func signed(t *testing.T, event, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/github", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	return r
}

func fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/issues-labeled.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func render(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testProvider() *Provider {
	return &Provider{
		Secret:       testSecret,
		Repositories: map[string]bool{"webgrip:ploeg-test": true},
		TeamMap:      map[string]string{"ploeg-bot": "silver"},
		Log:          slog.New(slog.DiscardHandler),
	}
}

func TestLabeledIssueIsAssignedToTheLabelsTeam(t *testing.T) {
	p := testProvider()
	r := signed(t, "issues", render(t, fixture(t)))
	events, err := p.ParseWebhook(r)
	if err != nil || len(events) != 1 {
		t.Fatalf("events %+v, err %v", events, err)
	}
	ev := events[0]
	if ev.Kind != provider.TrackerAssigned || ev.Team != "bronze" || ev.ExternalID != "webgrip:ploeg-test:42" || ev.Actor != "maintainer" {
		t.Fatalf("event %+v; want bronze assigned webgrip:ploeg-test:42 by maintainer", ev)
	}
	if ev.Scope != (provider.Scope{Kind: "repo", ID: "webgrip:ploeg-test", Name: "WebGrip/Ploeg-Test"}) {
		t.Fatalf("scope %+v", ev.Scope)
	}
	if !ev.At.Equal(time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("at %s", ev.At)
	}
	item := ev.Item
	if item.Provider != "github" || item.Title != "Add a health endpoint" || item.ExternalScope != "webgrip:ploeg-test" ||
		item.URL != "https://github.com/webgrip/ploeg-test/issues/42" || len(item.Labels) != 3 || item.Revision != "2026-10-11T09:30:00Z" {
		t.Fatalf("snapshot %+v", item)
	}
	if strings.Contains(item.Description, "push to main") || item.Description != "Expose GET /healthz.\n\n\nIt answers 200." {
		t.Fatalf("description %q; hidden HTML comments must not reach a Run", item.Description)
	}
	if got := p.DeliveryID(r); got != "72d3162e-cc78-11e3-81ab-4c9367dc0958" {
		t.Fatalf("delivery id %q", got)
	}
}

func TestIssueActionsMapToTrackerEvents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   string
		label    string
		assignee string
		state    string
		kind     provider.TrackerEventKind
		team     string
	}{
		{"team label removed", "unlabeled", "Ploeg:bronze", "", "open", provider.TrackerUnassigned, "bronze"},
		{"other label added", "labeled", "bug", "", "open", provider.TrackerUpdated, ""},
		{"bare prefix label", "labeled", "ploeg:", "", "open", provider.TrackerUpdated, ""},
		{"team label on a closed issue", "labeled", "ploeg:bronze", "", "closed", provider.TrackerUpdated, "bronze"},
		{"mapped assignee", "assigned", "", "Ploeg-Bot", "open", provider.TrackerAssigned, "silver"},
		{"mapped assignee removed", "unassigned", "", "ploeg-bot", "open", provider.TrackerUnassigned, "silver"},
		{"unmapped assignee", "assigned", "", "colleague", "open", provider.TrackerUpdated, ""},
		{"edited", "edited", "", "", "open", provider.TrackerUpdated, ""},
		{"reopened", "reopened", "", "", "open", provider.TrackerUpdated, ""},
		{"closed", "closed", "", "", "closed", provider.TrackerClosed, ""},
		{"deleted", "deleted", "", "", "open", provider.TrackerClosed, ""},
		{"transferred", "transferred", "", "", "open", provider.TrackerClosed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture(t)
			m["action"] = tc.action
			m["label"] = map[string]any{"name": tc.label}
			if tc.label == "" {
				delete(m, "label")
			}
			if tc.assignee != "" {
				m["assignee"] = map[string]any{"login": tc.assignee}
			}
			m["issue"].(map[string]any)["state"] = tc.state
			events, err := testProvider().ParseWebhook(signed(t, "issues", render(t, m)))
			if err != nil || len(events) != 1 {
				t.Fatalf("events %+v err %v", events, err)
			}
			if events[0].Kind != tc.kind || events[0].Team != tc.team {
				t.Fatalf("got %s for team %q; want %s for %q", events[0].Kind, events[0].Team, tc.kind, tc.team)
			}
		})
	}
}

func TestIgnoredDeliveriesYieldNoEvents(t *testing.T) {
	p := testProvider()
	other := fixture(t)
	other["repository"].(map[string]any)["full_name"] = "someone/else"
	pr := fixture(t)
	pr["issue"].(map[string]any)["pull_request"] = map[string]any{"url": "https://api.github.com/repos/WebGrip/Ploeg-Test/pulls/42"}
	milestoned := fixture(t)
	milestoned["action"] = "milestoned"
	for name, req := range map[string]*http.Request{
		"ping":                     signed(t, "ping", `{"zen":"Keep it logically awesome."}`),
		"issue comment":            signed(t, "issue_comment", render(t, fixture(t))),
		"unconfigured repository":  signed(t, "issues", render(t, other)),
		"pull request issue event": signed(t, "issues", render(t, pr)),
		"unused action":            signed(t, "issues", render(t, milestoned)),
	} {
		events, err := p.ParseWebhook(req)
		if err != nil || len(events) != 0 {
			t.Errorf("%s: events %+v err %v; want none", name, events, err)
		}
	}
}

func TestUnknownTeamLabelIsIgnoredWhenTeamsAreConfigured(t *testing.T) {
	p := testProvider()
	p.Teams = map[string]bool{"silver": true}
	events, err := p.ParseWebhook(signed(t, "issues", render(t, fixture(t))))
	if err != nil || len(events) != 1 || events[0].Kind != provider.TrackerUpdated {
		t.Fatalf("events %+v err %v; a label naming no configured team must not dispatch", events, err)
	}
}

func TestSignatureIsRequiredAndChecked(t *testing.T) {
	body := render(t, fixture(t))
	tampered := signed(t, "issues", body)
	tampered.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
	for name, tc := range map[string]struct {
		p *Provider
		r *http.Request
	}{
		"no secret":          {&Provider{Repositories: testProvider().Repositories}, signed(t, "issues", body)},
		"wrong signature":    {testProvider(), tampered},
		"sha1 header only":   {testProvider(), sha1Only(signed(t, "issues", body))},
		"oversized delivery": {testProvider(), signed(t, "issues", strings.Repeat(" ", maxPayloadBytes+1))},
	} {
		if _, err := tc.p.ParseWebhook(tc.r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func sha1Only(r *http.Request) *http.Request {
	r.Header.Set("X-Hub-Signature", r.Header.Get("X-Hub-Signature-256"))
	r.Header.Del("X-Hub-Signature-256")
	return r
}

type staticTokens struct {
	mu        sync.Mutex
	tokens    []string
	forgotten int
}

func (s *staticTokens) Token(context.Context, string, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[0], nil
}

func (s *staticTokens) Forget(string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten++
	if len(s.tokens) > 1 {
		s.tokens = s.tokens[1:]
	}
}

type fakeAPI struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    []string
	auths    []string
	comments []string
	patches  []map[string]string
	limited  int
	limitHdr http.Header
	state    string
	pages    int
}

func newAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{state: "open", pages: 1}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer expired" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if f.limited > 0 {
			f.limited--
			for k, v := range f.limitHdr {
				w.Header()[k] = v
			}
			http.Error(w, `{"message":"You have exceeded a secondary rate limit."}`, http.StatusForbidden)
			return
		}
		const issuePath = "/repos/webgrip/ploeg-test/issues/42"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == issuePath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 42, "title": "Add a health endpoint", "body": "Body<!-- hidden", "state": f.state,
				"html_url": "https://github.com/WebGrip/Ploeg-Test/issues/42", "updated_at": "2026-10-11T10:00:00Z",
				"created_at": "2026-10-09T08:00:00Z", "labels": []map[string]string{{"name": "ploeg:bronze"}, {"name": "repo/app"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/webgrip/ploeg-test/issues/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open", "pull_request": map[string]string{"url": "x"}})
		case r.Method == http.MethodPost && r.URL.Path == issuePath+"/comments":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.comments = append(f.comments, body["body"])
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPatch && r.URL.Path == issuePath:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.patches = append(f.patches, body)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == issuePath+"/comments":
			page := r.URL.Query().Get("page")
			if page == "" {
				page = "1"
			}
			var n int
			_, _ = fmt.Sscan(page, &n)
			if n < f.pages {
				w.Header().Set("Link", fmt.Sprintf(`<%s%s/comments?per_page=100&page=%d>; rel="next", <%s%s/comments?per_page=100&page=%d>; rel="last"`,
					f.srv.URL, issuePath, n+1, f.srv.URL, issuePath, f.pages))
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"body": "comment on page " + page, "created_at": "2026-10-10T00:00:00Z", "user": map[string]string{"login": "maintainer"}}})
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) provider(tokens TokenSource) *Provider {
	p := testProvider()
	p.APIURL = f.srv.URL
	p.Tokens = tokens
	return p
}

func TestFetchExecutionItemReadsTheIssueWithItsInstallationToken(t *testing.T) {
	api := newAPI(t)
	p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
	got, err := p.FetchExecutionItem(context.Background(), "webgrip:ploeg-test:42")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Open || got.Item.Title != "Add a health endpoint" || got.Item.Description != "Body" || got.Item.ExternalScope != "webgrip:ploeg-test" ||
		got.Item.Revision != "2026-10-11T10:00:00Z" || len(got.Item.Labels) != 2 || got.Item.TrackerCreatedAt.IsZero() || got.Item.Closed {
		t.Fatalf("item %+v open %t", got.Item, got.Open)
	}
	if api.auths[0] != "Bearer ghs_fixture" {
		t.Fatalf("authorization %q", api.auths[0])
	}
	api.state = "closed"
	got, err = p.FetchExecutionItem(context.Background(), "webgrip:ploeg-test:42")
	if err != nil || got.Open || !got.Item.Closed {
		t.Fatalf("closed issue read as open=%t closed=%t err=%v", got.Open, got.Item.Closed, err)
	}
	if _, err := p.FetchExecutionItem(context.Background(), "webgrip:ploeg-test:7"); err == nil {
		t.Fatal("a pull request was read as an issue")
	}
	if _, err := p.FetchExecutionItem(context.Background(), "webgrip/ploeg-test#42"); err == nil {
		t.Fatal("a malformed identity was read")
	}
	if _, err := testProvider().FetchExecutionItem(context.Background(), "webgrip:ploeg-test:42"); err == nil {
		t.Fatal("a provider without credentials read an issue")
	}
}

func TestCommentAndCloseWriteBack(t *testing.T) {
	api := newAPI(t)
	p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
	ctx := context.Background()
	if err := p.Comment(ctx, "webgrip:ploeg-test:42", "Ploeg finished this item."); err != nil {
		t.Fatal(err)
	}
	for _, s := range []work.State{work.StateNeedsHuman, work.StateAwaitingReview, work.StateStale} {
		if err := p.SetStatus(ctx, "webgrip:ploeg-test:42", s); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.patches) != 0 {
		t.Fatalf("a state other than done changed the issue: %+v", api.patches)
	}
	if err := p.SetStatus(ctx, "webgrip:ploeg-test:42", work.StateDone); err != nil {
		t.Fatal(err)
	}
	if len(api.comments) != 1 || api.comments[0] != "Ploeg finished this item." {
		t.Fatalf("comments %+v", api.comments)
	}
	if len(api.patches) != 1 || api.patches[0]["state"] != "closed" || api.patches[0]["state_reason"] != "completed" {
		t.Fatalf("patches %+v", api.patches)
	}
	unconfigured := testProvider()
	if err := unconfigured.Comment(ctx, "webgrip:ploeg-test:42", "x"); err != nil {
		t.Fatalf("unconfigured comment failed instead of skipping: %v", err)
	}
}

func TestBoardCommentsFollowsPagination(t *testing.T) {
	api := newAPI(t)
	api.pages = 3
	p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
	comments, err := p.BoardComments(context.Background(), "webgrip:ploeg-test:42")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 3 || comments[2].Text != "comment on page 3" || comments[0].Author != "maintainer" {
		t.Fatalf("comments %+v; want all three pages in order", comments)
	}
	status, err := p.BoardStatus(context.Background(), "webgrip:ploeg-test:42")
	if err != nil || status.Scope != "webgrip:ploeg-test" || len(status.Statuses) != 1 || status.Statuses[0] != "open" || len(status.Labels) != 2 {
		t.Fatalf("status %+v err %v", status, err)
	}
}

func TestNextLinkOutsideTheAPIRootIsRefused(t *testing.T) {
	api := newAPI(t)
	p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
	_, err := p.do(context.Background(), issueRef{"webgrip", "ploeg-test", 42}, http.MethodGet, "https://evil.example/steal", nil, nil)
	if err == nil || len(api.calls) != 0 {
		t.Fatalf("err %v after %d calls; a token must never leave the API root", err, len(api.calls))
	}
}

func TestSecondaryRateLimitIsWaitedOutWithinTheBound(t *testing.T) {
	api := newAPI(t)
	api.limited = 1
	api.limitHdr = http.Header{"Retry-After": {"0"}}
	p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
	if err := p.Comment(context.Background(), "webgrip:ploeg-test:42", "after the limit"); err != nil {
		t.Fatalf("a short Retry-After was not waited out: %v", err)
	}
	if len(api.comments) != 1 || len(api.calls) != 2 {
		t.Fatalf("calls %v comments %v", api.calls, api.comments)
	}
}

func TestLongRateLimitReturnsRateLimitError(t *testing.T) {
	for name, hdr := range map[string]http.Header{
		"retry-after":       {"Retry-After": {"120"}},
		"primary exhausted": {"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(time.Now().Add(30 * time.Minute).Unix())}},
	} {
		t.Run(name, func(t *testing.T) {
			api := newAPI(t)
			api.limited = 1
			api.limitHdr = hdr
			p := api.provider(&staticTokens{tokens: []string{"ghs_fixture"}})
			err := p.Comment(context.Background(), "webgrip:ploeg-test:42", "x")
			var rl *RateLimitError
			if !errors.As(err, &rl) || rl.RetryAfter < time.Minute {
				t.Fatalf("err %v; want a RateLimitError asking for the long wait", err)
			}
			if len(api.calls) != 1 {
				t.Fatalf("calls %v; a long limit must not be retried in the request", api.calls)
			}
		})
	}
}

func TestRejectedTokenIsForgottenAndRetriedOnce(t *testing.T) {
	api := newAPI(t)
	tokens := &staticTokens{tokens: []string{"expired", "ghs_fresh"}}
	p := api.provider(tokens)
	if err := p.Comment(context.Background(), "webgrip:ploeg-test:42", "x"); err != nil {
		t.Fatal(err)
	}
	if tokens.forgotten != 1 || api.auths[1] != "Bearer ghs_fresh" {
		t.Fatalf("forgotten %d auths %v", tokens.forgotten, api.auths)
	}
	stuck := &staticTokens{tokens: []string{"expired"}}
	if err := api.provider(stuck).Comment(context.Background(), "webgrip:ploeg-test:42", "x"); err == nil {
		t.Fatal("a token rejected twice was accepted")
	}
}

func TestTaskURLAndIdentity(t *testing.T) {
	if got := ScopeID("WebGrip/Ploeg"); got != "webgrip:ploeg" {
		t.Errorf("ScopeID = %q", got)
	}
	for _, bad := range []string{"", "webgrip", "/ploeg", "a/b/c"} {
		if got := ScopeID(bad); got != "" {
			t.Errorf("ScopeID(%q) = %q; want empty", bad, got)
		}
	}
	ghes := &Provider{APIURL: "https://git.example/api/v3"}
	if got := ghes.TaskURL("acme:app:9"); got != "https://git.example/acme/app/issues/9" {
		t.Errorf("Enterprise Server task URL %q", got)
	}
	if got := (&Provider{}).TaskURL("acme:app:x"); got != "" {
		t.Errorf("task URL for a bad id %q", got)
	}
	if got := work.Branch(work.WorkItem{Provider: Name, ExternalID: "webgrip:ploeg:42"}); got != "agent/github-webgrip-ploeg-42" {
		t.Errorf("branch %q", got)
	}
}

func TestStripHiddenText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                      "plain",
		"a <!-- x --> b <!--y-->c":   "a  b c",
		"keep <!-- unclosed\nsecret": "keep",
		"<!-- template -->\n\nReal":  "Real",
	} {
		if got := StripHiddenText(in); got != want {
			t.Errorf("StripHiddenText(%q) = %q, want %q", in, got, want)
		}
	}
}

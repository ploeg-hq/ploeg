package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

type fakeGitHub struct {
	t       *testing.T
	mu      sync.Mutex
	routes  map[string]func(w http.ResponseWriter, r *http.Request)
	anyAuth bool
	seen    []*http.Request
	bodies  []string
}

func newFake(t *testing.T) (*fakeGitHub, *Provider) {
	t.Helper()
	f := &fakeGitHub{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := &Provider{BaseURL: "https://github.com", APIURL: srv.URL, Tokens: githubapi.StaticToken("ghs_test"), Secret: "s3cret",
		Client: &githubapi.Client{Sleep: func(context.Context, time.Duration) error { return nil }}}
	return f, p
}

func (f *fakeGitHub) on(route string, h func(w http.ResponseWriter, r *http.Request)) {
	f.routes[route] = h
}

func (f *fakeGitHub) json(route string, status int, body string) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.seen = append(f.seen, r.Clone(context.Background()))
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	if got := r.Header.Get("Authorization"); !f.anyAuth && got != "Bearer ghs_test" {
		f.t.Errorf("%s %s authorization = %q", r.Method, r.URL.Path, got)
	}
	if r.Header.Get("X-GitHub-Api-Version") == "" {
		f.t.Errorf("%s %s names no API version", r.Method, r.URL.Path)
	}
	route := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		if h, ok := f.routes[route+"?"+r.URL.RawQuery]; ok {
			h(w, r)
			return
		}
	}
	if h, ok := f.routes[route]; ok {
		h(w, r)
		return
	}
	f.t.Errorf("unexpected request %s?%s", route, r.URL.RawQuery)
	w.WriteHeader(http.StatusNotFound)
}

func TestCommentPostsToTheIssueConversation(t *testing.T) {
	f, p := newFake(t)
	f.json("POST /repos/acme/widgets/issues/7/comments", http.StatusCreated, `{"id": 1}`)
	if err := p.Comment(context.Background(), "acme/widgets", 7, "findings"); err != nil {
		t.Fatal(err)
	}
	if f.bodies[0] != `{"body":"findings"}` {
		t.Fatalf("body = %s", f.bodies[0])
	}
	if err := p.Comment(context.Background(), "acme", 7, "x"); err == nil {
		t.Fatal("a repo without an owner was accepted")
	}
}

func TestCommentsFollowsTheLinkHeaderToTheLastPage(t *testing.T) {
	f, p := newFake(t)
	var next string
	f.on("GET /repos/acme/widgets/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"id": 3, "body": "marker"}]`)
			return
		}
		w.Header().Set("Link", `<`+next+`>; rel="next"`)
		_, _ = io.WriteString(w, `[{"id": 1, "body": "a"}, {"id": 2, "body": "b"}]`)
	})
	next = strings.TrimSuffix(p.APIURL, "/") + "/repos/acme/widgets/issues/7/comments?per_page=100&page=2"
	got, err := p.Comments(context.Background(), "acme/widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].ID != 3 || got[2].Body != "marker" {
		t.Fatalf("comments = %+v", got)
	}
}

func TestEditAndDeleteComment(t *testing.T) {
	f, p := newFake(t)
	f.json("PATCH /repos/acme/widgets/issues/comments/9", http.StatusOK, `{}`)
	f.json("DELETE /repos/acme/widgets/issues/comments/9", http.StatusNotFound, `{"message":"Not Found"}`)
	if err := p.EditComment(context.Background(), "acme/widgets", 7, 9, "new"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteComment(context.Background(), "acme/widgets", 7, 9); err != nil {
		t.Fatalf("deleting a gone comment: %v", err)
	}
}

func TestPullRequestMergeabilityReadsFactsFromOneRead(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"closed","merged":true,
		"merged_at":"2026-10-01T10:00:00Z","closed_at":"2026-10-01T10:00:00Z","created_at":"2026-09-30T08:00:00Z",
		"merge_commit_sha":"m1","merged_by":{"login":"ryan"},"user":{"login":"ploeg[bot]"},"draft":false,
		"head":{"ref":"agent/vik-1","sha":"h1"},"base":{"ref":"development"},"mergeable":null,
		"additions":10,"deletions":2,"changed_files":3}`)
	m, err := p.PullRequestMergeability(context.Background(), "acme/widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	facts := m.Facts
	if facts.State != provider.PullRequestMerged || facts.MergeCommitSHA != "m1" || facts.MergedBy != "ryan" || facts.HeadSHA != "h1" ||
		facts.Author != "ploeg[bot]" || *facts.ChangedFiles != 3 || facts.MergedAt == nil || m.BaseBranch != "development" || m.Mergeable != nil {
		t.Fatalf("mergeability = %+v facts = %+v", m, facts)
	}
}

func TestPullRequestMergeabilityReportsADirtyPullRequestAsConflicted(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"open","mergeable":null,"mergeable_state":"dirty",
		"head":{"ref":"agent/vik-1","sha":"h1"},"base":{"ref":"main"}}`)
	m, err := p.PullRequestMergeability(context.Background(), "acme/widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if m.Facts.State != provider.PullRequestOpen || m.Mergeable == nil || *m.Mergeable {
		t.Fatalf("mergeability = %+v", m)
	}
}

func TestReadsWaitOutASecondaryRateLimit(t *testing.T) {
	f, p := newFake(t)
	calls := 0
	f.on("GET /repos/acme/widgets/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
			return
		}
		_, _ = io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"h"}}`)
	})
	if state, err := p.PullRequestState(context.Background(), "acme/widgets", 7); err != nil || state != provider.PullRequestOpen {
		t.Fatalf("state %q err %v after a secondary rate limit", state, err)
	}
}

func TestCommitStatusCombinesStatusesAndCheckRuns(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/commits/abc/status", http.StatusOK,
		`{"state":"success","statuses":[{"context":"ci/legacy","state":"success"}]}`)
	f.json("GET /repos/acme/widgets/commits/abc/check-runs", http.StatusOK, `{"total_count":3,"check_runs":[
		{"name":"test","status":"completed","conclusion":"success"},
		{"name":"lint","status":"completed","conclusion":"skipped"},
		{"name":"build","status":"in_progress","conclusion":null}]}`)
	st, ok, err := p.CommitStatus(context.Background(), "acme/widgets", "abc")
	if err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if st.State != provider.CommitPending || len(st.Checks) != 4 {
		t.Fatalf("status = %+v", st)
	}
	f.json("GET /repos/acme/widgets/commits/abc/check-runs", http.StatusOK, `{"total_count":1,"check_runs":[
		{"name":"test","status":"completed","conclusion":"timed_out"}]}`)
	if st, _, _ := p.CommitStatus(context.Background(), "acme/widgets", "abc"); st.State != provider.CommitFailure {
		t.Fatalf("a timed-out check run combined to %q", st.State)
	}
}

func TestCommitStatusWithoutChecksIsUnknown(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/commits/abc/status", http.StatusOK, `{"state":"pending","statuses":[]}`)
	f.json("GET /repos/acme/widgets/commits/abc/check-runs", http.StatusOK, `{"total_count":0,"check_runs":[]}`)
	if _, ok, err := p.CommitStatus(context.Background(), "acme/widgets", "abc"); ok || err != nil {
		t.Fatalf("ok %v err %v: a commit without checks has no status", ok, err)
	}
}

func TestIsAncestorReadsTheCompareCount(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/compare/deploy...merge", http.StatusOK, `{"status":"behind","ahead_by":0,"behind_by":4,"total_commits":0,"commits":[]}`)
	f.json("GET /repos/acme/widgets/compare/old...merge", http.StatusOK, `{"status":"diverged","ahead_by":2,"total_commits":2,"commits":[{}]}`)
	if in, err := p.IsAncestor(context.Background(), "acme/widgets", "merge", "deploy"); err != nil || !in {
		t.Fatalf("in %v err %v", in, err)
	}
	if in, err := p.IsAncestor(context.Background(), "acme/widgets", "merge", "old"); err != nil || in {
		t.Fatalf("in %v err %v", in, err)
	}
}

func TestChangedPathsAndChange(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7/files", http.StatusOK, `[
		{"filename":"a.go","status":"modified","additions":3,"deletions":1},
		{"filename":"b.go","previous_filename":"old.go","status":"renamed","additions":0,"deletions":0},
		{"filename":"c.go","status":"removed","additions":0,"deletions":9}]`)
	f.json("GET /repos/acme/widgets/pulls/7/commits", http.StatusOK, `[{"commit":{"message":"feat: one"}},{"commit":{"message":"fix: two"}}]`)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"open","title":"T","body":"B","labels":[{"name":"agent"}]}`)
	paths, err := p.ChangedPaths(context.Background(), "acme/widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths.Paths) != 3 || paths.Paths[1].Status != provider.PathRenamed || paths.Paths[1].PreviousPath != "old.go" ||
		paths.Paths[2].Status != provider.PathDeleted {
		t.Fatalf("paths = %+v", paths)
	}
	change, err := p.PullRequestChange(context.Background(), "acme/widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if change.Title != "T" || len(change.Labels) != 1 || len(change.Files) != 4 || len(change.Commits) != 2 ||
		change.Lines["a.go"].Additions != 3 || change.Lines["c.go"].Deletions != 9 {
		t.Fatalf("change = %+v", change)
	}
}

func TestPullRequestDiffIsCutAtALine(t *testing.T) {
	f, p := newFake(t)
	f.on("GET /repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.diff" {
			t.Errorf("accept = %q", r.Header.Get("Accept"))
		}
		_, _ = io.WriteString(w, "line one\nline two\nline three\n")
	})
	diff, truncated, err := p.PullRequestDiff(context.Background(), "acme/widgets", 7, 15)
	if err != nil || !truncated || string(diff) != "line one\n" {
		t.Fatalf("diff %q truncated %v err %v", diff, truncated, err)
	}
}

func TestInspectRepository(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets", http.StatusOK, `{"archived":false,"mirror_url":null,"default_branch":"main"}`)
	f.json("GET /repos/acme/widgets/contents/AGENTS.md", http.StatusOK, `{"type":"file"}`)
	state, err := p.InspectRepository(context.Background(), "acme", "widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.Archived || state.Mirror || state.Branch != "main" || !state.AgentsFile {
		t.Fatalf("state = %+v", state)
	}
	if got, _ := p.RepositoryURL("acme", "widgets"); got != "https://github.com/acme/widgets.git" {
		t.Fatalf("repository URL = %q", got)
	}
}

func signed(t *testing.T, p *Provider, event, delivery string, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(p.Secret))
	mac.Write(body)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/github", strings.NewReader(string(body)))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if delivery != "" {
		r.Header.Set(DeliveryHeader, delivery)
	}
	return r
}

func pullPayload(action string, pr map[string]any) map[string]any {
	return map[string]any{"action": action, "repository": map[string]any{"full_name": "acme/widgets"},
		"sender": map[string]any{"login": "ryan"}, "pull_request": pr}
}

func TestParseWebhookRejectsWhatItCannotAuthenticate(t *testing.T) {
	_, p := newFake(t)
	r := signed(t, p, "ping", "d1", map[string]any{"zen": "x"})
	r.Header.Set("X-Hub-Signature-256", "sha256=00")
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("a bad signature was accepted")
	}
	r = signed(t, p, "ping", "d1", map[string]any{"zen": "x"})
	r.Header.Set("X-Hub-Signature-256", strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("a signature without its sha256= prefix was accepted")
	}
	if _, err := p.ParseWebhook(signed(t, p, "ping", "", map[string]any{"zen": "x"})); err == nil {
		t.Fatal("a delivery without an id was accepted: it could never be deduplicated")
	}
	noSecret := *p
	noSecret.Secret = ""
	if _, err := noSecret.ParseWebhook(signed(t, p, "ping", "d1", map[string]any{})); err == nil {
		t.Fatal("an unconfigured secret accepted a delivery")
	}
	events, err := p.ParseWebhook(signed(t, p, "ping", "d1", map[string]any{"zen": "x", "repository": map[string]any{"full_name": "acme/widgets"}}))
	if err != nil || len(events) != 0 {
		t.Fatalf("ping: events %v err %v", events, err)
	}
}

func TestParseWebhookSettlesAMergedPullRequest(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"closed","merged":true,"head":{"sha":"h1"}}`)
	events, err := p.ParseWebhook(signed(t, p, "pull_request", "d1", pullPayload("closed", map[string]any{
		"number": 7, "state": "closed", "merged": true, "merge_commit_sha": "m1", "merged_at": "2026-10-01T10:00:00Z",
		"head": map[string]any{"ref": "agent/vik-1", "sha": "h1"}})))
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v err %v", events, err)
	}
	ev := events[0]
	if ev.Kind != provider.ForgePRMerged || ev.PR != 7 || ev.Branch != "agent/vik-1" || ev.PullRequest.MergeCommitSHA != "m1" ||
		ev.PullRequest.MergedBy != "ryan" || ev.Repo != "acme/widgets" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseWebhookDropsAClosedEventForAPullRequestThatIsOpenAgain(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"open","head":{"sha":"h2"}}`)
	events, err := p.ParseWebhook(signed(t, p, "pull_request", "d-old", pullPayload("closed", map[string]any{
		"number": 7, "state": "closed", "merged": false, "head": map[string]any{"ref": "agent/vik-1", "sha": "h1"}})))
	if err != nil || len(events) != 0 {
		t.Fatalf("a replayed close settled an open pull request: events %v err %v", events, err)
	}
}

func TestParseWebhookNormalizesTheOtherPullRequestEvents(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/widgets/pulls/7", http.StatusOK, `{"number":7,"state":"closed","merged":false}`)
	head := map[string]any{"ref": "agent/vik-1", "sha": "h1"}
	cases := []struct {
		event, action string
		extra         map[string]any
		payload       map[string]any
		want          provider.ForgeEventKind
		review        provider.ForgeReviewState
	}{
		{event: "pull_request", action: "closed", extra: map[string]any{"state": "closed", "merged": false}, want: provider.ForgePRClosed},
		{event: "pull_request", action: "opened", extra: map[string]any{"state": "open"}, want: provider.ForgePROpened},
		{event: "pull_request", action: "synchronize", extra: map[string]any{"state": "open"}, want: provider.ForgePRSynchronized},
		{event: "pull_request", action: "edited", extra: map[string]any{"state": "open", "mergeable_state": "dirty"}, want: provider.ForgeMergeStateDirty},
		{event: "pull_request_review", action: "submitted", extra: map[string]any{"state": "open"},
			payload: map[string]any{"review": map[string]any{"state": "changes_requested", "body": "please fix"}},
			want:    provider.ForgeReviewSubmitted, review: provider.ForgeReviewChangesRequested},
	}
	for _, c := range cases {
		pr := map[string]any{"number": 7, "head": head}
		for k, v := range c.extra {
			pr[k] = v
		}
		payload := pullPayload(c.action, pr)
		for k, v := range c.payload {
			payload[k] = v
		}
		events, err := p.ParseWebhook(signed(t, p, c.event, "d", payload))
		if err != nil || len(events) != 1 || events[0].Kind != c.want || events[0].Review != c.review || events[0].PR != 7 {
			t.Errorf("%s/%s: events %+v err %v", c.event, c.action, events, err)
		}
	}
	events, _ := p.ParseWebhook(signed(t, p, "pull_request", "d", pullPayload("labeled", map[string]any{"number": 7, "state": "open", "head": head})))
	if len(events) != 0 {
		t.Fatalf("a labeled event produced %+v", events)
	}
}

func TestParseWebhookReportsFailedChecks(t *testing.T) {
	_, p := newFake(t)
	events, err := p.ParseWebhook(signed(t, p, "check_run", "d", map[string]any{"action": "completed",
		"repository": map[string]any{"full_name": "acme/widgets"},
		"check_run": map[string]any{"name": "test", "status": "completed", "conclusion": "failure", "head_sha": "h1",
			"html_url": "https://github.com/acme/widgets/runs/1", "pull_requests": []any{map[string]any{"number": 7}},
			"check_suite": map[string]any{"head_branch": "agent/vik-1"}}}))
	if err != nil || len(events) != 1 || events[0].Kind != provider.ForgeCheckFailed || events[0].PR != 7 || events[0].Branch != "agent/vik-1" ||
		!strings.Contains(events[0].Body, "test") {
		t.Fatalf("check_run: events %+v err %v", events, err)
	}
	events, _ = p.ParseWebhook(signed(t, p, "check_run", "d", map[string]any{"action": "completed",
		"repository": map[string]any{"full_name": "acme/widgets"},
		"check_run":  map[string]any{"name": "test", "status": "completed", "conclusion": "success"}}))
	if len(events) != 0 {
		t.Fatalf("a passing check run produced %+v", events)
	}
	events, err = p.ParseWebhook(signed(t, p, "status", "d", map[string]any{"state": "error", "context": "ci/legacy",
		"repository": map[string]any{"full_name": "acme/widgets"}, "branches": []any{map[string]any{"name": "agent/vik-1"}}}))
	if err != nil || len(events) != 1 || events[0].Kind != provider.ForgeCheckFailed || events[0].Branch != "agent/vik-1" {
		t.Fatalf("status: events %+v err %v", events, err)
	}
}

var (
	_ provider.ForgeProvider           = (*Provider)(nil)
	_ provider.MergeabilityReader      = (*Provider)(nil)
	_ provider.CommitStatusReader      = (*Provider)(nil)
	_ provider.AncestryReader          = (*Provider)(nil)
	_ provider.PullRequestChangeReader = (*Provider)(nil)
	_ provider.ChangedPathsReader      = (*Provider)(nil)
	_ provider.PullRequestDiffReader   = (*Provider)(nil)
	_ provider.CommentDeleter          = (*Provider)(nil)
	_ provider.RepositoryInspector     = (*Provider)(nil)
	_ provider.ForgeRepositoryLocator  = (*Provider)(nil)
)

type rotatingTokens struct{ forgotten int }

func (r *rotatingTokens) Token(context.Context, string, string) (string, error) {
	if r.forgotten == 0 {
		return "ghs_stale", nil
	}
	return "ghs_test", nil
}

func (r *rotatingTokens) Forget(string, string) { r.forgotten++ }

func TestAReadMintsAgainOnceWhenGitHubRefusesACachedToken(t *testing.T) {
	f, p := newFake(t)
	tokens := &rotatingTokens{}
	p.Tokens = tokens
	f.anyAuth = true
	f.on("GET /repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghs_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"h"}}`)
	})
	if state, err := p.PullRequestState(context.Background(), "acme/widgets", 7); err != nil || state != provider.PullRequestOpen {
		t.Fatalf("state %q err %v", state, err)
	}
	if tokens.forgotten != 1 {
		t.Fatalf("forgot the cached token %d times, want once", tokens.forgotten)
	}
}

func TestDeliveryIDIsTheGitHubDeliveryHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(DeliveryHeader, " d-42 ")
	if got := (&Provider{}).DeliveryID(r); got != "d-42" {
		t.Fatalf("delivery id = %q", got)
	}
}

package forgejo

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
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// The provider is the only place Forgejo's REST dialect is allowed to exist,
// so these tests are what keep the rest of the codebase honest about it.
// External services are faked with httptest and never need network.

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, p *Provider, event string, body any) ([]provider.ForgeEvent, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", strings.NewReader(string(b)))
	if event != "" {
		r.Header.Set("X-Forgejo-Event", event)
	}
	if p.Secret != "" {
		r.Header.Set("X-Forgejo-Signature", sign(p.Secret, b))
	}
	return p.ParseWebhook(r)
}

// A comment must land on the PR, through the issues endpoint — /pulls/{n}/
// comments would be a review comment on a diff hunk, which findings are not.
func TestComment_PostsToTheIssuesEndpoint(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "s3cret"}
	if err := p.Comment(context.Background(), "webgrip/ploeg", 7, "## security\n- token logged"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if want := "/api/v1/repos/webgrip/ploeg/issues/7/comments"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "token s3cret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent["body"], "token logged") {
		t.Errorf("body = %q, want the findings verbatim", sent["body"])
	}
}

// A failed publish must say why, and must never leak the token into the error.
func TestComment_SurfacesTheFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"token does not have permission"}`))
	}))
	defer srv.Close()

	err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).Comment(context.Background(), "webgrip/ploeg", 7, "x")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "permission") {
		t.Errorf("error does not explain the failure: %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestComment_RejectsMalformedTargets(t *testing.T) {
	p := &Provider{BaseURL: "http://example.invalid"}
	for _, tc := range []struct {
		repo string
		pr   int
	}{
		{"noslash", 1}, {"", 1}, {"webgrip/ploeg", 0}, {"webgrip/", 1},
	} {
		if err := p.Comment(context.Background(), tc.repo, tc.pr, "x"); err == nil {
			t.Errorf("Comment(%q, %d) was accepted", tc.repo, tc.pr)
		}
	}
}

// An unverified webhook is rejected BEFORE anything is parsed or touched
// (forge-provider-forgejo spec).
func TestParseWebhook_RejectsBadSignature(t *testing.T) {
	p := &Provider{Secret: "shh"}
	body := []byte(`{"action":"submitted","repository":{"full_name":"webgrip/ploeg"}}`)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	r.Header.Set("X-Forgejo-Signature", sign("wrong-secret", body))
	if _, err := p.ParseWebhook(r); err == nil {
		t.Fatal("a wrongly-signed webhook was accepted")
	}
	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	if _, err := p.ParseWebhook(r2); err == nil {
		t.Fatal("an unsigned webhook was accepted")
	}
}

func TestParseWebhook_ReviewSubmitted(t *testing.T) {
	p := &Provider{Secret: "shh"}
	events, err := post(t, p, "pull_request_review", map[string]any{
		"action":     "reviewed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{
			"number": 12,
			"head":   map[string]any{"ref": "agent/vik-585"},
		},
		"review": map[string]any{
			"type":    "pull_request_review_rejected",
			"content": "the retry loop is still unbounded",
		},
	})
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Kind != provider.ForgeReviewSubmitted || e.Repo != "webgrip/ploeg" || e.PR != 12 ||
		e.Branch != "agent/vik-585" || !strings.Contains(e.Body, "unbounded") {
		t.Errorf("event = %+v", e)
	}
}

func TestParseWebhook_CheckFailed(t *testing.T) {
	p := &Provider{Secret: "shh"}
	events, err := post(t, p, "status", map[string]any{
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"state":      "failure",
		"branches":   []string{"agent/vik-585"},
		"commit":     map[string]any{"message": "go vet failed"},
	})
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(events) != 1 || events[0].Kind != provider.ForgeCheckFailed ||
		events[0].Branch != "agent/vik-585" {
		t.Fatalf("event = %+v", events)
	}
}

func TestParseWebhook_MergeStateDirty(t *testing.T) {
	p := &Provider{Secret: "shh"}
	events, err := post(t, p, "pull_request", map[string]any{
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{
			"number":          12,
			"head":            map[string]any{"ref": "agent/vik-585"},
			"mergeable_state": "dirty",
		},
	})
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(events) != 1 || events[0].Kind != provider.ForgeMergeStateDirty {
		t.Fatalf("event = %+v", events)
	}
}

// A forge subscribes wider than the core consumes. An irrelevant event is
// dropped quietly — erroring would turn every unrelated push into a failed
// delivery and eventually a disabled webhook (spec scenario).
func TestParseWebhook_DropsIrrelevantEventsQuietly(t *testing.T) {
	p := &Provider{Secret: "shh"}
	for _, body := range []map[string]any{
		{"action": "push", "repository": map[string]any{"full_name": "webgrip/ploeg"}},
		{"repository": map[string]any{"full_name": "webgrip/ploeg"}, "state": "success"},
		{"action": "label_updated", "repository": map[string]any{"full_name": "webgrip/ploeg"},
			"pull_request": map[string]any{"number": 1, "mergeable_state": "clean"}},
	} {
		events, err := post(t, p, "push", body)
		if err != nil {
			t.Errorf("irrelevant event errored: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("irrelevant event produced %+v", events)
		}
	}
}

// Core code must never see a Forgejo field name. This is the R7 guard the
// spec asks for, enforced where it can actually be checked: the normalized
// event carries only SPI types.
func TestParseWebhook_EmitsOnlySPITypes(t *testing.T) {
	p := &Provider{Secret: "shh"}
	events, _ := post(t, p, "pull_request_review", map[string]any{
		"repository":   map[string]any{"full_name": "webgrip/ploeg"},
		"pull_request": map[string]any{"number": 3, "head": map[string]any{"ref": "b"}},
		"review":       map[string]any{"type": "pull_request_review_comment", "content": "x"},
	})
	if len(events) != 1 {
		t.Fatal("expected one event")
	}
	var _ provider.ForgeEvent = events[0]
	if events[0].Repo != "webgrip/ploeg" {
		t.Errorf("repo not normalized: %+v", events[0])
	}
}

func TestParseWebhook_ClosedPullRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		merged    bool
		mergeable bool
		want      provider.ForgeEventKind
	}{
		{"merged", true, false, provider.ForgePRMerged},
		{"closed without merging", false, true, provider.ForgePRClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := post(t, &Provider{Secret: "shh"}, "pull_request", map[string]any{
				"action":     "closed",
				"number":     12,
				"repository": map[string]any{"full_name": "webgrip/ploeg"},
				"pull_request": map[string]any{
					"number": 12, "merged": tc.merged, "mergeable": tc.mergeable,
					"head": map[string]any{"ref": "agent/vik-585"},
				},
			})
			if err != nil {
				t.Fatalf("ParseWebhook: %v", err)
			}
			if len(events) != 1 || events[0].Kind != tc.want || events[0].PR != 12 ||
				events[0].Repo != "webgrip/ploeg" || events[0].Branch != "agent/vik-585" {
				t.Fatalf("events = %+v, want one %s", events, tc.want)
			}
		})
	}
}

// ADR-0045: the merge facts in the payload reach the event.
func TestParseWebhook_MergedPullRequestCarriesItsFacts(t *testing.T) {
	events, err := post(t, &Provider{Secret: "shh"}, "pull_request", map[string]any{
		"action":     "closed",
		"number":     12,
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": "ryan"},
		"pull_request": map[string]any{
			"number": 12, "merged": true,
			"head":             map[string]any{"ref": "agent/vik-585", "sha": "1111aaaa"},
			"merge_commit_sha": "2222bbbb",
			"merged_at":        "2026-10-01T09:30:00+02:00",
			"closed_at":        "2026-10-01T09:30:00+02:00",
			"merged_by":        map[string]any{"login": "anna"},
		},
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	f := events[0].PullRequest
	want := time.Date(2026, 10, 1, 7, 30, 0, 0, time.UTC)
	if f.State != provider.PullRequestMerged || f.HeadSHA != "1111aaaa" || f.MergeCommitSHA != "2222bbbb" ||
		f.MergedBy != "anna" || f.MergedAt == nil || !f.MergedAt.Equal(want) || events[0].Actor != "ryan" {
		t.Errorf("facts = %+v actor %q", f, events[0].Actor)
	}
}

func TestParseWebhook_ClosedPullRequestHasNoMergeFacts(t *testing.T) {
	events, err := post(t, &Provider{Secret: "shh"}, "pull_request", map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": "ryan"},
		"pull_request": map[string]any{
			"number": 12, "merged": false, "closed_at": "2026-10-01T10:00:00Z",
			"head": map[string]any{"ref": "agent/vik-585", "sha": "1111aaaa"},
		},
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	f := events[0].PullRequest
	if f.State != provider.PullRequestClosed || f.ClosedAt == nil || f.MergedAt != nil || f.MergedBy != "" || f.MergeCommitSHA != "" {
		t.Errorf("facts = %+v", f)
	}
}

func TestParseWebhook_ReviewCarriesVerdictReviewerAndHead(t *testing.T) {
	for reviewType, want := range map[string]provider.ForgeReviewState{
		"pull_request_review_approved": provider.ForgeReviewApproved,
		"pull_request_review_rejected": provider.ForgeReviewChangesRequested,
		"pull_request_review_comment":  provider.ForgeReviewCommented,
	} {
		events, err := post(t, &Provider{Secret: "shh"}, "pull_request_review", map[string]any{
			"repository":   map[string]any{"full_name": "webgrip/ploeg"},
			"sender":       map[string]any{"login": "anna"},
			"pull_request": map[string]any{"number": 12, "head": map[string]any{"ref": "agent/vik-585", "sha": "3333cccc"}},
			"review":       map[string]any{"type": reviewType, "content": "ok"},
		})
		if err != nil || len(events) != 1 {
			t.Fatalf("%s: events = %+v, %v", reviewType, events, err)
		}
		e := events[0]
		if e.Review != want || e.Actor != "anna" || e.PullRequest.HeadSHA != "3333cccc" || e.PullRequest.State != "" {
			t.Errorf("%s: event = %+v", reviewType, e)
		}
	}
}

func TestPullRequestFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"closed","merged":true,"merge_commit_sha":"2222bbbb",
			"merged_at":"2026-10-01T09:30:00Z","closed_at":"2026-10-01T09:30:00Z",
			"merged_by":{"login":"anna"},"head":{"sha":"1111aaaa"}}`))
	}))
	defer srv.Close()
	f, err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).PullRequestFacts(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != provider.PullRequestMerged || f.HeadSHA != "1111aaaa" || f.MergeCommitSHA != "2222bbbb" ||
		f.MergedBy != "anna" || f.MergedAt == nil || f.MergedAt.Format(time.RFC3339) != "2026-10-01T09:30:00Z" {
		t.Errorf("facts = %+v", f)
	}

	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged":false,"merged_at":null,"merged_by":null,"head":{"sha":"9999"}}`))
	}))
	defer open.Close()
	f, err = (&Provider{BaseURL: open.URL}).PullRequestFacts(context.Background(), "webgrip/ploeg", 7)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != provider.PullRequestOpen || f.HeadSHA != "9999" || f.MergedAt != nil || f.ClosedAt != nil || f.MergedBy != "" {
		t.Errorf("open facts = %+v", f)
	}
}

func TestParseWebhook_ClosedIssueIsDropped(t *testing.T) {
	events, err := post(t, &Provider{Secret: "shh"}, "issues", map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"issue":      map[string]any{"number": 4},
	})
	if err != nil || len(events) != 0 {
		t.Fatalf("closed issue produced %+v, %v", events, err)
	}
}

func TestPullRequestState(t *testing.T) {
	for _, tc := range []struct {
		body string
		want provider.PullRequestState
	}{
		{`{"state":"open","merged":false}`, provider.PullRequestOpen},
		{`{"state":"closed","merged":true}`, provider.PullRequestMerged},
		{`{"state":"closed","merged":false}`, provider.PullRequestClosed},
	} {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
			_, _ = w.Write([]byte(tc.body))
		}))
		got, err := (&Provider{BaseURL: srv.URL, Token: "s3cret"}).PullRequestState(context.Background(), "webgrip/ploeg", 7)
		srv.Close()
		if err != nil {
			t.Fatalf("PullRequestState(%s): %v", tc.body, err)
		}
		if got != tc.want {
			t.Errorf("PullRequestState(%s) = %q, want %q", tc.body, got, tc.want)
		}
		if gotPath != "/api/v1/repos/webgrip/ploeg/pulls/7" || gotAuth != "token s3cret" {
			t.Errorf("request = %q with auth %q", gotPath, gotAuth)
		}
	}
}

func TestPullRequestState_SurfacesTheFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "s3cret"}
	_, err := p.PullRequestState(context.Background(), "webgrip/ploeg", 7)
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.PullRequestState(context.Background(), "ploeg", 7); err == nil {
		t.Error("a repo without an owner was accepted")
	}
}

// With no secret configured, nothing can be verified, so nothing is accepted:
// an unsigned delivery must not reach code that settles items or opens paid
// follow-ups.
func TestParseWebhook_WithoutASecretRejectsEveryDelivery(t *testing.T) {
	body := `{"repository":{"full_name":"webgrip/ploeg"},"state":"failure","branches":["agent/vik-1"]}`
	for name, signature := range map[string]string{
		"unsigned":  "",
		"signed":    sign("", []byte(body)),
		"arbitrary": "deadbeef",
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", strings.NewReader(body))
			r.Header.Set("X-Forgejo-Event", "status")
			if signature != "" {
				r.Header.Set("X-Forgejo-Signature", signature)
			}
			events, err := (&Provider{}).ParseWebhook(r)
			if err == nil {
				t.Fatalf("an unverifiable delivery was accepted: %+v", events)
			}
		})
	}
}

// Comments follows pagination to exhaustion, so a marker on any page is
// returned and the caller's find-by-marker scan cannot miss it.
func TestForgejoComments_PagesToExhaustion(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "1" {
			var items []map[string]any
			for i := 1; i <= commentsPageSize; i++ {
				items = append(items, map[string]any{"id": i, "body": "noise"})
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		_, _ = w.Write([]byte(`[{"id":51,"body":"<!-- ploeg:usage-report -->"}]`))
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}
	got, err := p.Comments(context.Background(), "webgrip/ploeg", 5)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != commentsPageSize+1 || got[len(got)-1].Body != "<!-- ploeg:usage-report -->" {
		t.Fatalf("comments = %d, last = %+v", len(got), got[len(got)-1])
	}
	if len(pages) != 2 {
		t.Errorf("pages fetched = %v, want two", pages)
	}
}

func TestForgejoEditComment_SendsBody(t *testing.T) {
	var gotPath, gotMethod, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}
	if err := p.EditComment(context.Background(), "webgrip/ploeg", 5, 42, "updated"); err != nil {
		t.Fatalf("EditComment: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/v1/repos/webgrip/ploeg/issues/comments/42" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotAuth != "token s3cret" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"body":"updated"`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestForgejoEditComment_FailureSurfacesWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"no permission"}`))
	}))
	defer srv.Close()
	err := (&Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}).EditComment(context.Background(), "webgrip/ploeg", 5, 42, "x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestForgejoComments_SurfacesFailureWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad token"}`))
	}))
	defer srv.Close()
	_, err := (&Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}).Comments(context.Background(), "webgrip/ploeg", 5)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

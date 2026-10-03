package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// The provider is the only place GitLab's REST dialect is allowed to exist,
// so these tests are what keep the rest of the codebase honest about it.
// External services are faked with httptest and never need network.

func post(t *testing.T, p *Provider, token string, body any) ([]provider.ForgeEvent, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/gitlab", strings.NewReader(string(b)))
	if token == "" {
		token = p.Secret
	}
	if token != "" {
		r.Header.Set("X-Gitlab-Token", token)
	}
	return p.ParseWebhook(r)
}

// A note must land on the merge request through the NOTES endpoint, and the
// project path must arrive URL-encoded as a single segment — GitLab 404s a
// raw slash.
func TestCommentPostsNoteToEncodedProjectPath(t *testing.T) {
	var gotPath, gotToken, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}
	if err := p.Comment(context.Background(), "group/sub/proj", 7, "findings"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if want := "/api/v4/projects/group%2Fsub%2Fproj/merge_requests/7/notes"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotToken != "tok" {
		t.Errorf("PRIVATE-TOKEN = %q, want tok", gotToken)
	}
	if !strings.Contains(gotBody, `"body":"findings"`) {
		t.Errorf("body = %q, want it to carry the note", gotBody)
	}
}

func TestCommentRejectsBadInput(t *testing.T) {
	p := &Provider{BaseURL: "http://example.invalid"}
	for _, tc := range []struct {
		name string
		repo string
		mr   int
	}{
		{"no namespace", "proj", 1},
		{"empty segment", "group//proj", 1},
		{"empty repo", "", 1},
		{"zero iid", "group/proj", 0},
		{"negative iid", "group/proj", -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Comment(context.Background(), tc.repo, tc.mr, "x"); err == nil {
				t.Fatal("want an error before a request is spent")
			}
		})
	}
}

func TestCommentSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
	}))
	defer srv.Close()
	p := &Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}
	err := p.Comment(context.Background(), "g/p", 1, "x")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error %q should carry the status", err)
	}
}

// GitLab echoes a shared secret rather than signing the body, so the token is
// the whole authentication story — a wrong one must be refused.
func TestParseWebhookRejectsWrongToken(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	_, err := post(t, p, "nope", map[string]any{"object_kind": "note"})
	if err == nil {
		t.Fatal("want an error for a mismatched X-Gitlab-Token")
	}
}

func TestParseWebhookAcceptsRightToken(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	if _, err := post(t, p, "s3cret", map[string]any{"object_kind": "push"}); err != nil {
		t.Fatalf("want the delivery accepted, got %v", err)
	}
}

// A note on a merge request is review feedback; a note on an issue is not,
// and both arrive on the same hook.
func TestParseWebhookNoteOnMergeRequest(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind":       "note",
		"project":           map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{"note": "please fix the nil deref"},
		"merge_request":     map[string]any{"iid": 12, "source_branch": "feat/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	got := evs[0]
	if got.Kind != provider.ForgeReviewSubmitted || got.Repo != "g/p" || got.PR != 12 ||
		got.Branch != "feat/x" || got.Body != "please fix the nil deref" {
		t.Errorf("unexpected event %+v", got)
	}
}

func TestParseWebhookNoteOnIssueIsDropped(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind":       "note",
		"project":           map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{"note": "unrelated"},
	})
	if err != nil {
		t.Fatalf("an unrelated note must be dropped, not an error: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("want no events, got %+v", evs)
	}
}

// GitLab has no review object: approval arrives as a merge_request action.
func TestParseWebhookApprovalIsReviewSubmitted(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{
			"iid": 4, "action": "approved", "source_branch": "feat/y",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != provider.ForgeReviewSubmitted || evs[0].PR != 4 {
		t.Fatalf("unexpected %+v", evs)
	}
}

func TestParseWebhookMergeConflict(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{
			"iid": 9, "action": "update", "merge_status": "cannot_be_merged", "source_branch": "feat/z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != provider.ForgeMergeStateDirty || evs[0].PR != 9 {
		t.Fatalf("unexpected %+v", evs)
	}
}

// A mergeable MR update is noise: subscribing wider than the core consumes
// must not produce events.
func TestParseWebhookMergeableUpdateIsDropped(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{
			"iid": 9, "action": "update", "merge_status": "can_be_merged",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("want no events, got %+v", evs)
	}
}

func TestParseWebhookFailedPipeline(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind":       "pipeline",
		"project":           map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{"status": "failed", "ref": "feat/w"},
		"merge_request":     map[string]any{"iid": 21, "source_branch": "feat/w"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != provider.ForgeCheckFailed || evs[0].PR != 21 || evs[0].Branch != "feat/w" {
		t.Fatalf("unexpected %+v", evs)
	}
}

// A branch pipeline has no merge request. PR 0 is the honest answer — the core
// reads it as "nothing to route this to", not as merge request zero.
func TestParseWebhookBranchPipelineHasNoMergeRequest(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind":       "pipeline",
		"project":           map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{"status": "failed", "ref": "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].PR != 0 || evs[0].Branch != "main" {
		t.Fatalf("unexpected %+v", evs)
	}
}

func TestParseWebhookSuccessfulPipelineIsDropped(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{
		"object_kind":       "pipeline",
		"project":           map[string]any{"path_with_namespace": "g/p"},
		"object_attributes": map[string]any{"status": "success"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("want no events, got %+v", evs)
	}
}

func TestParseWebhookWithoutProjectIsDropped(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	evs, err := post(t, p, "", map[string]any{"object_kind": "note"})
	if err != nil {
		t.Fatalf("want it dropped, got %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("want no events, got %+v", evs)
	}
}

func TestName(t *testing.T) {
	if (&Provider{}).Name() != "gitlab" {
		t.Fatal("Name must stay stable: it is the webhook route and the map key")
	}
}

func TestParseWebhookMergeRequestClosed(t *testing.T) {
	for action, want := range map[string]provider.ForgeEventKind{
		"merge": provider.ForgePRMerged,
		"close": provider.ForgePRClosed,
	} {
		evs, err := post(t, &Provider{Secret: "s3cret"}, "", map[string]any{
			"object_kind": "merge_request",
			"project":     map[string]any{"path_with_namespace": "g/p"},
			"object_attributes": map[string]any{
				"iid": 9, "action": action, "merge_status": "cannot_be_merged", "source_branch": "feat/z",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != want || evs[0].PR != 9 || evs[0].Branch != "feat/z" {
			t.Errorf("action %q: unexpected %+v", action, evs)
		}
	}
}

// ADR-0045: the merge request's head, merge commit and merger reach the
// event; a timestamp the payload lacks stays unknown.
func TestParseWebhookMergeCarriesItsFacts(t *testing.T) {
	evs, err := post(t, &Provider{Secret: "s3cret"}, "", map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"path_with_namespace": "g/p"},
		"user":        map[string]any{"username": "anna"},
		"object_attributes": map[string]any{
			"iid": 9, "action": "merge", "source_branch": "agent/vik-1",
			"merge_commit_sha": "2222bbbb", "last_commit": map[string]any{"id": "1111aaaa"},
		},
	})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	f := evs[0].PullRequest
	if f.State != provider.PullRequestMerged || f.HeadSHA != "1111aaaa" || f.MergeCommitSHA != "2222bbbb" ||
		f.MergedBy != "anna" || f.MergedAt != nil || evs[0].Actor != "anna" {
		t.Errorf("facts = %+v actor %q", f, evs[0].Actor)
	}
}

func TestParseWebhookApprovalCarriesHead(t *testing.T) {
	evs, err := post(t, &Provider{Secret: "s3cret"}, "", map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"path_with_namespace": "g/p"},
		"user":        map[string]any{"username": "anna"},
		"object_attributes": map[string]any{
			"iid": 9, "action": "approved", "source_branch": "agent/vik-1", "last_commit": map[string]any{"id": "1111aaaa"},
		},
	})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if evs[0].Review != provider.ForgeReviewApproved || evs[0].Actor != "anna" || evs[0].PullRequest.HeadSHA != "1111aaaa" {
		t.Errorf("event = %+v", evs[0])
	}
}

func TestPullRequestFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"merged","sha":"1111aaaa","merge_commit_sha":null,"squash_commit_sha":"3333cccc",
			"merged_at":"2026-10-01T09:30:00.000Z","merge_user":{"username":"anna"},"merged_by":{"username":"old"}}`))
	}))
	defer srv.Close()
	f, err := (&Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}).PullRequestFacts(context.Background(), "group/sub/proj", 7)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != provider.PullRequestMerged || f.HeadSHA != "1111aaaa" || f.MergeCommitSHA != "3333cccc" ||
		f.MergedBy != "anna" || f.MergedAt == nil || f.MergedAt.Format("2006-01-02T15:04:05Z07:00") != "2026-10-01T09:30:00Z" {
		t.Errorf("facts = %+v", f)
	}
}

func TestPullRequestState(t *testing.T) {
	for state, want := range map[string]provider.PullRequestState{
		"opened": provider.PullRequestOpen,
		"locked": provider.PullRequestOpen,
		"merged": provider.PullRequestMerged,
		"closed": provider.PullRequestClosed,
	} {
		var gotPath, gotToken string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotToken = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")
			_, _ = w.Write([]byte(`{"state":"` + state + `"}`))
		}))
		got, err := (&Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}).PullRequestState(context.Background(), "group/sub/proj", 7)
		srv.Close()
		if err != nil {
			t.Fatalf("state %q: %v", state, err)
		}
		if got != want {
			t.Errorf("state %q = %q, want %q", state, got, want)
		}
		if gotPath != "/api/v4/projects/group%2Fsub%2Fproj/merge_requests/7" || gotToken != "tok" {
			t.Errorf("request = %q with token %q", gotPath, gotToken)
		}
	}
}

// With no secret configured, nothing can be verified, so nothing is accepted.
func TestParseWebhookWithoutASecretRejectsEveryDelivery(t *testing.T) {
	for _, token := range []string{"", "anything"} {
		if evs, err := post(t, &Provider{}, token, map[string]any{
			"object_kind":       "note",
			"project":           map[string]any{"path_with_namespace": "g/p"},
			"object_attributes": map[string]any{"note": "please fix"},
			"merge_request":     map[string]any{"iid": 12, "source_branch": "feat/x"},
		}); err == nil {
			t.Errorf("token %q: an unverifiable delivery was accepted: %+v", token, evs)
		}
	}
}

// Comments follows per_page pagination to exhaustion, so a marker on any page
// is returned and the caller's find-by-marker scan cannot miss it.
func TestGitlabComments_PagesToExhaustion(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "1" {
			var items []map[string]any
			for i := 1; i <= notesPerPage; i++ {
				items = append(items, map[string]any{"id": i, "body": "noise"})
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		_, _ = w.Write([]byte(`[{"id":101,"body":"<!-- ploeg:usage-report -->"}]`))
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}
	got, err := p.Comments(context.Background(), "group/sub/proj", 7)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != notesPerPage+1 || got[len(got)-1].Body != "<!-- ploeg:usage-report -->" {
		t.Fatalf("comments = %d, last = %+v", len(got), got[len(got)-1])
	}
	if len(pages) != 2 {
		t.Errorf("pages fetched = %v, want two", pages)
	}
}

func TestGitlabEditComment_SendsBody(t *testing.T) {
	var gotPath, gotMethod, gotBody, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotToken = r.URL.EscapedPath(), r.Method, r.Header.Get("PRIVATE-TOKEN")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &Provider{BaseURL: srv.URL, Token: "tok", HC: srv.Client()}
	if err := p.EditComment(context.Background(), "group/sub/proj", 7, 42, "updated"); err != nil {
		t.Fatalf("EditComment: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v4/projects/group%2Fsub%2Fproj/merge_requests/7/notes/42" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotToken != "tok" {
		t.Errorf("PRIVATE-TOKEN = %q", gotToken)
	}
	if !strings.Contains(gotBody, `"body":"updated"`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestGitlabEditComment_FailureSurfacesWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
	}))
	defer srv.Close()
	err := (&Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}).EditComment(context.Background(), "group/proj", 7, 42, "x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

func TestGitlabComments_SurfacesFailureWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	}))
	defer srv.Close()
	_, err := (&Provider{BaseURL: srv.URL, Token: "s3cret", HC: srv.Client()}).Comments(context.Background(), "group/proj", 7)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type keyedForge struct {
	mu        sync.Mutex
	dialect   string
	base      string
	comments  map[int64]string
	order     []int64
	nextID    int64
	creates   int
	edits     int
	deletes   int
	uploads   int
	failWrite bool
}

var (
	forgejoNotes  = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/(\d+)/comments$`)
	forgejoNote   = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/comments/(\d+)$`)
	forgejoAssets = regexp.MustCompile(`^/api/v1/repos/webgrip/ploeg/issues/comments/(\d+)/assets$`)
	gitlabNotes   = regexp.MustCompile(`^/api/v4/projects/webgrip/ploeg/merge_requests/(\d+)/notes$`)
	gitlabNote    = regexp.MustCompile(`^/api/v4/projects/webgrip/ploeg/merge_requests/(\d+)/notes/(\d+)$`)
	gitlabUploads = regexp.MustCompile(`^/api/v4/projects/webgrip/ploeg/uploads$`)
)

func newKeyedForge(t *testing.T, dialect string) *keyedForge {
	t.Helper()
	f := &keyedForge{dialect: dialect, comments: map[int64]string{}, nextID: 500}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.base = srv.URL
	return f
}

func (f *keyedForge) list(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if r.URL.Query().Get("page") == "1" {
		for _, id := range f.order {
			out = append(out, map[string]any{"id": id, "body": f.comments[id]})
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *keyedForge) create(w http.ResponseWriter, r *http.Request) {
	if f.failWrite {
		http.Error(w, "nope", http.StatusForbidden)
		return
	}
	f.creates++
	var body struct{ Body string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.nextID++
	f.comments[f.nextID] = body.Body
	f.order = append(f.order, f.nextID)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextID, "body": body.Body})
}

func (f *keyedForge) edit(w http.ResponseWriter, r *http.Request, id int64) {
	f.edits++
	var body struct{ Body string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.comments[id] = body.Body
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "body": body.Body})
}

func (f *keyedForge) remove(w http.ResponseWriter, id int64) {
	f.deletes++
	delete(f.comments, id)
	var keep []int64
	for _, c := range f.order {
		if c != id {
			keep = append(keep, c)
		}
	}
	f.order = keep
	w.WriteHeader(http.StatusNoContent)
}

func (f *keyedForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case (forgejoNotes.MatchString(p) || gitlabNotes.MatchString(p)) && r.Method == http.MethodGet:
		f.list(w, r)
	case (forgejoNotes.MatchString(p) || gitlabNotes.MatchString(p)) && r.Method == http.MethodPost:
		f.create(w, r)
	case forgejoNote.MatchString(p):
		id, _ := strconv.ParseInt(forgejoNote.FindStringSubmatch(p)[1], 10, 64)
		if r.Method == http.MethodDelete {
			f.remove(w, id)
		} else {
			f.edit(w, r, id)
		}
	case gitlabNote.MatchString(p):
		id, _ := strconv.ParseInt(gitlabNote.FindStringSubmatch(p)[2], 10, 64)
		if r.Method == http.MethodDelete {
			f.remove(w, id)
		} else {
			f.edit(w, r, id)
		}
	case forgejoAssets.MatchString(p) && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`[]`))
	case forgejoAssets.MatchString(p) && r.Method == http.MethodPost:
		f.uploads++
		_, _ = io.Copy(io.Discard, r.Body)
		f.nextID++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextID, "name": "comment.svg",
			"browser_download_url": fmt.Sprintf("%s/attachments/%d", f.base, f.nextID)})
	case gitlabUploads.MatchString(p) && r.Method == http.MethodPost:
		f.uploads++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "/uploads/abc/comment.svg"})
	default:
		http.NotFound(w, r)
	}
}

func (f *keyedForge) bodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range f.order {
		out = append(out, f.comments[id])
	}
	return out
}

func keyedCommentServer(t *testing.T, dialect string, execute bool) (*Server, *keyedForge, string, int64) {
	t.Helper()
	s, token := factsServer(t, []string{"silver"}, execute)
	forge := newKeyedForge(t, dialect)
	switch dialect {
	case "forgejo":
		s.Forges = map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: forge.base, Log: slog.New(slog.DiscardHandler)}}
	case "gitlab":
		s.Forges = map[string]provider.ForgeProvider{"gitlab": &gitlab.Provider{BaseURL: forge.base, Log: slog.New(slog.DiscardHandler)}}
	}
	id, pr := plainFactsItem(t, "keyed-"+dialect, "silver", 21, "anna", time.Now().UTC())
	execSQL(t, `UPDATE pull_requests SET forge = $2 WHERE id = $1`, pr, dialect)
	return s, forge, token, id
}

const safeSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="#123"/></svg>`

func actorHeaders() map[string]string { return map[string]string{"X-Ploeg-Actor": "anna"} }

type commentBodyView struct {
	Comment struct {
		Key         string `json:"key"`
		CommentID   *int64 `json:"commentId"`
		ImageURL    *string
		Created     bool `json:"created"`
		PullRequest struct {
			Forge  string `json:"forge"`
			Number int    `json:"number"`
		} `json:"pullRequest"`
	} `json:"comment"`
}

func putComment(t *testing.T, s *Server, token string, id int64, key, body string) commentBodyView {
	t.Helper()
	w := operatorDo(t, s, token, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/%s", id, key), body, actorHeaders())
	if w.Code != 200 {
		t.Fatalf("put %s: %d %s", key, w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	var v commentBodyView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPullRequestComment_UpsertsOneCommentPerKeyAndDeletesIt(t *testing.T) {
	for _, dialect := range []string{"forgejo", "gitlab"} {
		t.Run(dialect, func(t *testing.T) {
			s, forge, token, id := keyedCommentServer(t, dialect, true)
			body, _ := json.Marshal(map[string]any{"markdown": "| a | b |\n| - | - |", "image": map[string]any{"svg": safeSVG, "alt": "summary"}})
			first := putComment(t, s, token, id, "summary", string(body))
			if !first.Comment.Created || first.Comment.CommentID == nil || first.Comment.PullRequest.Forge != dialect || first.Comment.PullRequest.Number != 21 {
				t.Fatalf("first = %+v", first)
			}
			bodies := forge.bodies()
			if len(bodies) != 1 || !strings.HasPrefix(bodies[0], "<!-- ploeg:comment:summary -->\n![summary](") || !strings.HasSuffix(bodies[0], "| - | - |") {
				t.Fatalf("forge comments = %q", bodies)
			}
			second := putComment(t, s, token, id, "summary", `{"markdown":"updated"}`)
			if second.Comment.Created || *second.Comment.CommentID != *first.Comment.CommentID {
				t.Errorf("second = %+v", second)
			}
			putComment(t, s, token, id, "other", `{"markdown":"another key"}`)
			if bodies := forge.bodies(); len(bodies) != 2 || bodies[0] != "<!-- ploeg:comment:summary -->\nupdated" {
				t.Errorf("after update = %q", bodies)
			}
			if forge.creates != 2 || forge.uploads != 1 {
				t.Errorf("creates %d uploads %d", forge.creates, forge.uploads)
			}
			if n := scalar[int](t, `SELECT count(*) FROM audit_log WHERE action = 'pull_request_comment.upserted' AND actor = 'operator:workbench:anna'`); n != 3 {
				t.Errorf("upsert audits = %d", n)
			}

			w := operatorDo(t, s, token, "DELETE", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/summary", id), "", actorHeaders())
			if w.Code != 200 {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			validateOperatorSchema(t, w.Body.Bytes())
			if bodies := forge.bodies(); len(bodies) != 1 || !strings.Contains(bodies[0], "ploeg:comment:other") || forge.deletes != 1 {
				t.Errorf("after delete = %q (deletes %d)", bodies, forge.deletes)
			}
			if n := scalar[int](t, `SELECT count(*) FROM pull_request_comments WHERE key = 'summary'`); n != 0 {
				t.Errorf("summary record left: %d", n)
			}
			if n := scalar[int](t, `SELECT count(*) FROM audit_log WHERE action = 'pull_request_comment.deleted'`); n != 1 {
				t.Errorf("delete audits = %d", n)
			}
		})
	}
}

func TestPullRequestComment_AdoptsAnExistingPloegComment(t *testing.T) {
	s, forge, token, id := keyedCommentServer(t, "forgejo", true)
	forge.mu.Lock()
	forge.nextID++
	legacy := forge.nextID
	forge.comments[legacy] = "<!-- ploeg:run-card -->\nold card"
	forge.nextID++
	human := forge.nextID
	forge.comments[human] = "a person's comment"
	forge.order = append(forge.order, legacy, human)
	forge.mu.Unlock()

	w := operatorDo(t, s, token, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/card", id),
		fmt.Sprintf(`{"markdown":"x","adoptCommentId":%d}`, human), actorHeaders())
	if w.Code != 422 || !strings.Contains(w.Body.String(), "adopt_not_found") {
		t.Fatalf("adopting a person's comment: %d %s", w.Code, w.Body)
	}
	v := putComment(t, s, token, id, "card", fmt.Sprintf(`{"markdown":"taken over","adoptCommentId":%d}`, legacy))
	if v.Comment.Created || *v.Comment.CommentID != legacy || forge.creates != 0 {
		t.Fatalf("adopt = %+v creates %d", v, forge.creates)
	}
	if got := forge.bodies()[0]; got != "<!-- ploeg:comment:card -->\ntaken over" {
		t.Errorf("adopted body = %q", got)
	}
}

func TestPullRequestComment_Refusals(t *testing.T) {
	s, forge, token, id := keyedCommentServer(t, "forgejo", true)
	path := fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/", id)
	cases := []struct {
		name, key, body string
		headers         map[string]string
		want            int
		code            string
	}{
		{"no actor", "k", `{"markdown":"x"}`, nil, 400, "actor_required"},
		{"bad key", "Bad_Key", `{"markdown":"x"}`, actorHeaders(), 400, "invalid_key"},
		{"empty markdown", "k", `{"markdown":""}`, actorHeaders(), 400, "invalid_request"},
		{"marker smuggled", "k", `{"markdown":"<!-- ploeg:comment:other -->"}`, actorHeaders(), 400, "invalid_request"},
		{"unknown field", "k", `{"markdown":"x","extra":1}`, actorHeaders(), 400, "invalid_request"},
		{"script svg", "k", `{"markdown":"x","image":{"svg":"<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>"}}`, actorHeaders(), 422, "unsafe_svg"},
		{"handler svg", "k", `{"markdown":"x","image":{"svg":"<svg xmlns=\"http://www.w3.org/2000/svg\" onload=\"x()\"/>"}}`, actorHeaders(), 422, "unsafe_svg"},
		{"unknown pull request", "k", `{"markdown":"x","number":999}`, actorHeaders(), 404, "not_found"},
		{"too large", "k", `{"markdown":"` + strings.Repeat("x", 400*1024) + `"}`, actorHeaders(), 413, "too_large"},
	}
	for _, tc := range cases {
		w := operatorDo(t, s, token, "PUT", path+tc.key, tc.body, tc.headers)
		if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
	if forge.creates+forge.edits+forge.uploads != 0 {
		t.Errorf("a refused request wrote to the forge")
	}
	readOnly, _, readToken, readID := keyedCommentServer(t, "forgejo", false)
	if w := operatorDo(t, readOnly, readToken, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/k", readID),
		`{"markdown":"x"}`, actorHeaders()); w.Code != 403 {
		t.Errorf("read-only consumer: %d %s", w.Code, w.Body)
	}
	gold, _ := plainFactsItem(t, "keyed-gold", "gold", 22, "anna", time.Now().UTC())
	if w := operatorDo(t, readOnly, readToken, "DELETE", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/k", gold), "", actorHeaders()); w.Code != 403 {
		t.Errorf("read-only delete: %d", w.Code)
	}
	s2, _, token2, _ := keyedCommentServer(t, "forgejo", true)
	gold2, _ := plainFactsItem(t, "keyed-gold-2", "gold", 23, "anna", time.Now().UTC())
	if w := operatorDo(t, s2, token2, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/k", gold2), `{"markdown":"x"}`, actorHeaders()); w.Code != 404 {
		t.Errorf("other team: %d %s", w.Code, w.Body)
	}
	bare, _, err := testStore.IngestAssigned(t.Context(), work.WorkItem{Provider: "vikunja", ExternalID: "keyed-bare", Team: "silver", Title: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if w := operatorDo(t, s2, token2, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/k", bare), `{"markdown":"x"}`, actorHeaders()); w.Code != 409 {
		t.Errorf("no pull request: %d %s", w.Code, w.Body)
	}
}

func TestPullRequestComment_ForgeRefusalIsReported(t *testing.T) {
	s, forge, token, id := keyedCommentServer(t, "gitlab", true)
	forge.failWrite = true
	w := operatorDo(t, s, token, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/k", id), `{"markdown":"x"}`, actorHeaders())
	if w.Code != 502 || !strings.Contains(w.Body.String(), "forge_unavailable") {
		t.Fatalf("forge refusal: %d %s", w.Code, w.Body)
	}
	if n := scalar[int](t, `SELECT count(*) FROM pull_request_comments`); n != 0 {
		t.Errorf("a refused comment was recorded")
	}
}

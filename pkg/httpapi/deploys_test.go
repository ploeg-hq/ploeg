package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const deployToken = "deploy-token-0123456789abcdef0123456789"

func sha(c string) string { return strings.Repeat(c, 40) }

type compareForge struct {
	mu        sync.Mutex
	responses map[string]string
	calls     []string
}

func (f *compareForge) answer(base, head, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[base[:1]+"..."+head[:1]] = body
}

func (f *compareForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	basehead, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/webgrip/ploeg/compare/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	base, head, _ := strings.Cut(basehead, "...")
	key := base[:1] + "..." + head[:1]
	f.calls = append(f.calls, key)
	body, ok := f.responses[key]
	if !ok || body == "" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(body))
}

func (f *compareForge) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string{}, f.calls...)
	f.calls = nil
	sort.Strings(out)
	return out
}

func deployServer(t *testing.T, forge http.Handler) (*Server, string) {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(forge)
	t.Cleanup(srv.Close)
	auth, err := NewDeployAuth(deployToken)
	if err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	fj := &forgejo.Provider{BaseURL: srv.URL, Log: slog.New(slog.DiscardHandler)}
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler), Deploys: auth,
		Forges:         map[string]provider.ForgeProvider{"forgejo": fj, "forgejo-main": fj},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

func deployBody(environment, commit string) map[string]any {
	return map[string]any{
		"environment": environment,
		"repo":        map[string]any{"forge": "forgejo", "owner": "webgrip", "name": "ploeg"},
		"sha":         commit,
		"url":         "https://ci.example/webgrip/ploeg/actions/runs/7",
		"source":      "ci",
	}
}

func postDeploy(t *testing.T, s *Server, auth string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deploys", bytes.NewReader(raw))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func deploySchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/deploy-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validates(t *testing.T, schema *jsonschema.Schema, raw []byte) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("violates deploy-api.v1: %v\n%s", err, raw)
	}
}

type deployAnswer struct {
	DeployID     string `json:"deployId"`
	PullRequests int    `json:"pullRequests"`
}

func accepted(t *testing.T, w *httptest.ResponseRecorder) deployAnswer {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	validates(t, deploySchema(t), w.Body.Bytes())
	var a deployAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDeploys_DisabledWithoutAToken(t *testing.T) {
	s, _ := deployServer(t, http.NotFoundHandler())
	s.Deploys = nil
	w := postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("d")))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "PLOEG_DEPLOY_TOKEN") {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
	validates(t, deploySchema(t), w.Body.Bytes())
}

func TestDeploys_RequireTheDeployToken(t *testing.T) {
	s, operatorToken := deployServer(t, http.NotFoundHandler())
	for name, auth := range map[string]string{
		"missing":        "",
		"wrong":          "Bearer deploy-token-0123456789abcdef0123456780",
		"operator token": "Bearer " + operatorToken,
		"basic":          "Basic " + deployToken,
		"no credential":  "Bearer ",
	} {
		w := postDeploy(t, s, auth, deployBody("production", sha("d")))
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status %d (%s)", name, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deploys", strings.NewReader(`{}`))
	r.Header.Add("Authorization", "Bearer "+deployToken)
	r.Header.Add("Authorization", "Bearer "+deployToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("two Authorization headers: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/deploys", nil)
	r.Header.Set("Authorization", "Bearer "+deployToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET: %d", w.Code)
	}
	var rows int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM deployments`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows = %d, %v; a refused report records nothing", rows, err)
	}
}

func TestNewDeployAuth_RefusesAWeakToken(t *testing.T) {
	for _, token := range []string{"short", strings.Repeat("a", 31), strings.Repeat("a", 32) + " b", strings.Repeat("a", 4097)} {
		if _, err := NewDeployAuth(token); err == nil {
			t.Errorf("token of %d bytes accepted", len(token))
		}
	}
}

func TestDeploys_ValidateTheReport(t *testing.T) {
	s, _ := deployServer(t, http.NotFoundHandler())
	with := func(key string, value any) map[string]any {
		b := deployBody("production", sha("d"))
		if value == nil {
			delete(b, key)
		} else {
			b[key] = value
		}
		return b
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for name, tc := range map[string]struct {
		body   any
		status int
	}{
		"not json":              {[]byte(`environment=production`), 400},
		"two objects":           {[]byte(`{"environment":"production"} {}`), 400},
		"unknown field":         {with("deployed_at", "2026-10-01T10:00:00Z"), 400},
		"no environment":        {with("environment", nil), 400},
		"bad environment":       {with("environment", "prod env"), 400},
		"no repo":               {with("repo", nil), 400},
		"repo without forge":    {with("repo", map[string]any{"owner": "webgrip", "name": "ploeg"}), 400},
		"repo name with slash":  {with("repo", map[string]any{"forge": "forgejo", "owner": "webgrip", "name": "a/b"}), 400},
		"short sha":             {with("sha", "abc123"), 400},
		"non-hex sha":           {with("sha", sha("z")), 400},
		"bad deployedAt":        {with("deployedAt", "yesterday"), 400},
		"future deployedAt":     {with("deployedAt", future), 400},
		"ftp url":               {with("url", "ftp://ci.example/run"), 400},
		"relative url":          {with("url", "/runs/7"), 400},
		"unknown source":        {with("source", "argo"), 400},
		"unconfigured forge":    {with("repo", map[string]any{"forge": "github", "owner": "webgrip", "name": "ploeg"}), 422},
		"oversized":             {[]byte(`{"url":"` + strings.Repeat("a", 17<<10) + `"}`), 413},
		"environment is a list": {with("environment", []string{"production"}), 400},
	} {
		w := postDeploy(t, s, "Bearer "+deployToken, tc.body)
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", name, w.Code, tc.status, w.Body)
			continue
		}
		validates(t, deploySchema(t), w.Body.Bytes())
	}
	var rows int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM deployments`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows = %d, %v; an invalid report records nothing", rows, err)
	}
}

func TestDeploys_NormalizeAndStoreTheReport(t *testing.T) {
	forge := &compareForge{responses: map[string]string{}}
	s, _ := deployServer(t, forge)
	body := map[string]any{
		"environment": " Production ",
		"repo":        map[string]any{"forge": "forgejo-main", "owner": "WebGrip", "name": "Ploeg"},
		"sha":         strings.ToUpper(sha("d")),
		"deployedAt":  "2026-10-01T12:00:00+02:00",
		"url":         "https://bot:secret@ci.example/runs/7?token=x#log",
	}
	validates(t, deploySchema(t), mustJSON(t, body))
	a := accepted(t, postDeploy(t, s, "Bearer "+deployToken, body))
	if a.DeployID == "" || a.PullRequests != 0 {
		t.Errorf("answer = %+v", a)
	}
	var forgeName, owner, name, environment, commit, link string
	var source *string
	var at time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT forge, repo_owner, repo_name, environment, sha, url, source, deployed_at
		FROM deployments`).Scan(&forgeName, &owner, &name, &environment, &commit, &link, &source, &at); err != nil {
		t.Fatal(err)
	}
	if forgeName != "forgejo" || owner != "webgrip" || name != "ploeg" || environment != "production" || commit != sha("d") ||
		link != "https://ci.example/runs/7" || source != nil || !at.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("stored %s %s/%s %s %s %s %v %v", forgeName, owner, name, environment, commit, link, source, at)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mergedPullRequest(t *testing.T, item int64, number int, mergeCommit string, mergedAt time.Time) {
	t.Helper()
	if ok, err := testStore.RecordPullRequestFacts(context.Background(), store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg",
		Number: number, WorkItemID: item, State: "merged", MergeCommitSHA: mergeCommit, MergedAt: &mergedAt, MergedBy: "ryan"}); err != nil || !ok {
		t.Fatalf("recorded = %v, err = %v", ok, err)
	}
}

func firstDeploys(t *testing.T) map[string]time.Time {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT p.number, pd.environment, pd.first_deployed_at
		FROM pull_request_deployments pd JOIN pull_requests p ON p.id = pd.pull_request_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var number int
		var environment string
		var at time.Time
		if err := rows.Scan(&number, &environment, &at); err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprintf("%d/%s", number, environment)] = at.UTC()
	}
	return out
}

func TestDeploys_MarkMergedPullRequestsWhoseMergeCommitIsAnAncestor(t *testing.T) {
	forge := &compareForge{responses: map[string]string{}}
	s, operatorToken := deployServer(t, forge)
	item, _ := factsItem(t)
	now := time.Now().UTC().Truncate(time.Second)
	mergedPullRequest(t, item, 18, sha("1"), now.Add(-3*time.Hour))
	mergedPullRequest(t, item, 19, sha("2"), now.Add(-2*time.Hour))
	mergedPullRequest(t, item, 20, sha("3"), now.Add(-time.Hour))
	forge.answer(sha("d"), sha("1"), `{"total_commits":0,"commits":[]}`)
	forge.answer(sha("d"), sha("2"), `{"total_commits":2,"commits":[{"sha":"x"},{"sha":"2"}]}`)

	firstAt := now.Add(-30 * time.Minute)
	body := deployBody("production", sha("d"))
	body["deployedAt"] = firstAt.Format(time.RFC3339)
	first := accepted(t, postDeploy(t, s, "Bearer "+deployToken, body))
	if first.PullRequests != 1 {
		t.Errorf("first deploy marked %d; want the one ancestor", first.PullRequests)
	}
	if got := fmt.Sprint(forge.took()); got != "[d...1 d...2 d...3]" {
		t.Errorf("compared %s", got)
	}
	if got := firstDeploys(t); len(got) != 1 || !got["18/production"].Equal(firstAt) {
		t.Errorf("marked = %v; the diverged and the unreadable pull request stay unmarked", got)
	}

	repeat := accepted(t, postDeploy(t, s, "Bearer "+deployToken, body))
	if repeat.DeployID != first.DeployID || repeat.PullRequests != 0 {
		t.Errorf("repeat = %+v; want deploy %s again and nothing new", repeat, first.DeployID)
	}
	if got := fmt.Sprint(forge.took()); got != "[d...3]" {
		t.Errorf("repeat compared %s; only the failed comparison is retried", got)
	}

	forge.answer(sha("e"), sha("2"), `{"total_commits":0,"commits":[]}`)
	forge.answer(sha("e"), sha("3"), `{"total_commits":0,"commits":[]}`)
	later := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("e"))))
	if later.PullRequests != 2 || later.DeployID == first.DeployID {
		t.Errorf("later deploy = %+v", later)
	}
	if got := fmt.Sprint(forge.took()); got != "[e...2 e...3]" {
		t.Errorf("later deploy compared %s", got)
	}
	marks := firstDeploys(t)
	if !marks["18/production"].Equal(firstAt) || len(marks) != 3 {
		t.Errorf("marks = %v; the first time per environment is kept", marks)
	}

	forge.answer(sha("e"), sha("1"), `{"total_commits":0,"commits":[]}`)
	test := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("test", sha("e"))))
	if test.PullRequests != 3 {
		t.Errorf("test deploy marked %d; environments are marked separately", test.PullRequests)
	}

	var audits int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'deploy.recorded'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Errorf("audit rows = %d; one per recorded deploy", audits)
	}

	raw := operatorSchemaGET(t, s, operatorToken, fmt.Sprintf("work-items/%d/facts", item))
	var facts struct {
		Facts struct {
			PullRequests []struct {
				Number      int `json:"number"`
				Deployments []struct {
					Environment     string    `json:"environment"`
					FirstDeployedAt time.Time `json:"firstDeployedAt"`
					URL             string    `json:"url"`
				} `json:"deployments"`
			} `json:"pullRequests"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	var environments []string
	for _, p := range facts.Facts.PullRequests {
		if p.Number != 18 {
			continue
		}
		for _, d := range p.Deployments {
			environments = append(environments, d.Environment)
		}
		if len(p.Deployments) == 0 || !p.Deployments[0].FirstDeployedAt.Equal(firstAt) ||
			p.Deployments[0].URL != "https://ci.example/webgrip/ploeg/actions/runs/7" {
			t.Errorf("deployments of 18 = %+v", p.Deployments)
		}
	}
	if fmt.Sprint(environments) != "[production test]" {
		t.Errorf("deployments of 18 = %v; earliest first", environments)
	}
}

func TestDeploys_AForgeWithoutCompareMarksNothing(t *testing.T) {
	s, _ := deployServer(t, http.NotFoundHandler())
	s.Forges["forgejo"] = statuslessForge{}
	item, _ := factsItem(t)
	mergedPullRequest(t, item, 18, sha("1"), time.Now().Add(-time.Hour))
	if a := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("d")))); a.PullRequests != 0 {
		t.Errorf("marked %d", a.PullRequests)
	}
	if got := firstDeploys(t); len(got) != 0 {
		t.Errorf("marks = %v", got)
	}
}

type statuslessForge struct{}

func (statuslessForge) Name() string                                              { return "forgejo" }
func (statuslessForge) ParseWebhook(*http.Request) ([]provider.ForgeEvent, error) { return nil, nil }
func (statuslessForge) Comment(context.Context, string, int, string) error {
	return nil
}
func (statuslessForge) Comments(context.Context, string, int) ([]provider.Comment, error) {
	return nil, nil
}
func (statuslessForge) EditComment(context.Context, string, int, int64, string) error {
	return nil
}
func (statuslessForge) PullRequestState(context.Context, string, int) (provider.PullRequestState, error) {
	return provider.PullRequestMerged, nil
}
func (statuslessForge) PullRequestFacts(context.Context, string, int) (provider.PullRequestFacts, error) {
	return provider.PullRequestFacts{}, nil
}

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func operatorDo(t *testing.T, s *Server, token, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func factsServer(t *testing.T, teams []string, execute bool) (*Server, string) {
	t.Helper()
	reset(t)
	consumers, token := operatorTestConsumers(t, teams, execute)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler), ForgeBots: []string{"Ploeg-Bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}, "gold": {"builder"}}},
	}, token
}

func execSQL(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func scalar[T any](t *testing.T, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func plainFactsItem(t *testing.T, externalID, team string, number int, mergedBy string, at time.Time) (int64, int64) {
	t.Helper()
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team,
		Title: "facts " + externalID, Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, `UPDATE work_items SET created_at = $2 WHERE id = $1`, id, at.Add(-time.Hour))
	pr := scalar[int64](t, `INSERT INTO pull_requests (forge, repo_owner, repo_name, number, work_item_id, state, merged_at, merged_by, first_seen_at)
		VALUES ('forgejo', 'webgrip', 'ploeg', $1, $2, 'merged', $3, $4, $3) RETURNING id`, number, id, at, mergedBy)
	return id, pr
}

func richFactsItem(t *testing.T) (int64, int64) {
	t.Helper()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	id, pr := plainFactsItem(t, "4242", "silver", 42, "anna", at.Add(6*time.Hour))
	shift := scalar[int64](t, `INSERT INTO shifts (work_item_id, team, branch, budget) VALUES ($1, 'silver', 'agent/vik-4242', 5) RETURNING id`, id)
	execSQL(t, `INSERT INTO agent_runs (work_item_id, shift_id, team, role, round, writes, state, started_at, finished_at, outcome, run_token, usage, links)
		VALUES ($1, $2, 'silver', 'builder', 1, true, 'finished', $3, $4, 'pr_opened', '0123456789abcdef0123456789abcdef0123456789abcdef',
			'{"inputTokens": 1200, "outputTokens": 300, "costUsd": 0.42, "durationMs": 61000}', ARRAY['https://forge.example/webgrip/ploeg/pulls/42'])`,
		id, shift, at, at.Add(time.Hour))
	execSQL(t, `UPDATE work_items SET tracker_created_at = $2, estimate_seconds = 7200 WHERE id = $1`, id, at.Add(-48*time.Hour))
	execSQL(t, `UPDATE pull_requests SET shift_id = $2, branch = 'agent/vik-4242', base_branch = 'development', head_sha = $3,
		merge_commit_sha = $4, opened_at = $5, author = 'ploeg-bot', draft = false, additions = 30, deletions = 4, changed_files = 2,
		labels = ARRAY['hotfix'], commits = 3, first_commit_at = $5, force_pushes = 1, activity_captured_at = $6, activity_truncated = false,
		merge_state = 'clean', merge_head_sha = $3, merge_checked_at = $6, ci_state = 'success', ci_checks = '[{"context":"verify","state":"success"}]',
		ci_head_sha = $3, ci_captured_at = $6, ci_runs_captured_at = $6, ci_runs_source = 'actions', ci_runs_truncated = false,
		files_captured_at = $6, files_truncated = false, kpis = '{"secret":"derived"}' WHERE id = $1`,
		pr, shift, strings.Repeat("a", 40), strings.Repeat("b", 40), at.Add(time.Hour), at.Add(5*time.Hour))
	execSQL(t, `INSERT INTO pull_request_reviews (pull_request_id, reviewer, state, head_sha, received_at) VALUES
		($1, 'bram', 'changes_requested', $2, $3), ($1, 'Bram', 'approved', $2, $4)`, pr, strings.Repeat("a", 40), at.Add(2*time.Hour), at.Add(4*time.Hour))
	execSQL(t, `INSERT INTO pull_request_events (pull_request_id, kind, actor, at, state, head_sha) VALUES
		($1, 'push', 'carla', $2, NULL, NULL), ($1, 'comment', 'dirk', $3, NULL, NULL), ($1, 'review', 'bram', $3, 'approved', NULL)`,
		pr, at.Add(90*time.Minute), at.Add(3*time.Hour))
	execSQL(t, `INSERT INTO pull_request_ci_runs (pull_request_id, run_key, head_sha, workflow, status, created_at, started_at, completed_at, jobs)
		VALUES ($1, 'run-7', $2, 'ci', 'success', $3, $3, $4,
			'[{"name":"test","status":"success","startedAt":"2026-10-01T10:00:00Z","completedAt":"2026-10-01T10:05:00Z","queuedSeconds":12,"attempt":2}]')`,
		pr, strings.Repeat("a", 40), at.Add(time.Hour), at.Add(70*time.Minute))
	execSQL(t, `INSERT INTO pull_request_files (pull_request_id, path, additions, deletions, indent_method, indent_unit, indent_added, indent_removed, indent_max_depth)
		VALUES ($1, 'pkg/a.go', 20, 4, 'indentation/2026.1', 1, 33, 5, 4), ($1, 'README.md', 10, 0, NULL, NULL, NULL, NULL, NULL)`, pr)
	execSQL(t, `INSERT INTO pull_request_path_captures (pull_request_id, head_sha, truncated) VALUES ($1, $2, false)`, pr, strings.Repeat("a", 40))
	execSQL(t, `INSERT INTO pull_request_paths (pull_request_id, head_sha, position, path, status, previous_path) VALUES
		($1, $2, 0, 'pkg/a.go', 'modified', NULL), ($1, $2, 1, 'README.md', 'renamed', 'OLD.md')`, pr, strings.Repeat("a", 40))
	execSQL(t, `INSERT INTO pull_request_reverts (pull_request_id, forge, repo_owner, repo_name, number, merged_at, merged_by, matched_by)
		VALUES ($1, 'forgejo', 'webgrip', 'ploeg', 50, $2, 'erik', 'title')`, pr, at.Add(30*time.Hour))
	dep := scalar[int64](t, `INSERT INTO deployments (forge, repo_owner, repo_name, environment, sha, deployed_at, source)
		VALUES ('forgejo', 'webgrip', 'ploeg', 'production', $1, $2, 'gitops') RETURNING id`, strings.Repeat("b", 40), at.Add(8*time.Hour))
	execSQL(t, `INSERT INTO deployments (forge, repo_owner, repo_name, environment, sha, deployed_at) VALUES ('forgejo', 'webgrip', 'ploeg', 'staging', $1, $2)`,
		strings.Repeat("c", 40), at.Add(-24*time.Hour))
	execSQL(t, `INSERT INTO pull_request_deployments (pull_request_id, environment, deployment_id, first_deployed_at) VALUES ($1, 'production', $2, $3)`,
		pr, dep, at.Add(8*time.Hour))
	execSQL(t, `INSERT INTO status_transitions (work_item_id, status, gate, at, observed) VALUES ($1, 'Doing', 'development', $2, false), ($1, 'Review', NULL, $3, true)`,
		id, at, at.Add(2*time.Hour))
	execSQL(t, `INSERT INTO gate_transitions (work_item_id, gate, status, actor, reason, at) VALUES ($1, 'test', 'Test', 'fenna', NULL, $2), ($1, 'development', 'Doing', 'fenna', 'defect', $3)`,
		id, at.Add(7*time.Hour), at.Add(9*time.Hour))
	execSQL(t, `INSERT INTO work_item_epics (work_item_id, provider, epic_external_id, epic_title) VALUES ($1, 'vikunja', '100', 'The epic')`, id)
	execSQL(t, `INSERT INTO audit_log (actor, action, work_item_id) VALUES ('operator:workbench:anna', 'work_item.withdrawn', $1)`, id)
	return id, pr
}

type factsBody struct {
	SchemaVersion string `json:"schemaVersion"`
	Facts         struct {
		WorkItem struct {
			ID               string  `json:"id"`
			ExternalRef      string  `json:"externalRef"`
			EstimateSeconds  *int64  `json:"estimateSeconds"`
			TrackerCreatedAt *string `json:"trackerCreatedAt"`
			Epics            []struct {
				ExternalID string `json:"externalId"`
			} `json:"epics"`
			Withdrawals []struct {
				Actor string `json:"actor"`
			} `json:"withdrawals"`
		} `json:"workItem"`
		ActivityAt   time.Time         `json:"activityAt"`
		Runs         []json.RawMessage `json:"runs"`
		Shifts       []json.RawMessage `json:"shifts"`
		PullRequests []struct {
			Number   int    `json:"number"`
			URL      string `json:"url"`
			MergedBy string `json:"mergedBy"`
			Files    []struct {
				Path        string `json:"path"`
				Indentation *struct {
					Added    int `json:"added"`
					MaxDepth int `json:"maxDepth"`
				} `json:"indentation"`
			} `json:"files"`
			CIRuns []struct {
				Jobs []struct {
					Attempt int `json:"attempt"`
				} `json:"jobs"`
			} `json:"ciRuns"`
			Reviews      []json.RawMessage `json:"reviews"`
			Events       []json.RawMessage `json:"events"`
			Reverts      []json.RawMessage `json:"reverts"`
			Deployments  []json.RawMessage `json:"deployments"`
			ChangedPaths *struct {
				Paths []json.RawMessage `json:"paths"`
			} `json:"changedPaths"`
		} `json:"pullRequests"`
		StatusTransitions  []json.RawMessage `json:"statusTransitions"`
		GateTransitions    []json.RawMessage `json:"gateTransitions"`
		DeployEnvironments []struct {
			Environment string `json:"environment"`
		} `json:"deployEnvironments"`
		Roster []struct {
			Login string   `json:"login"`
			Roles []string `json:"roles"`
		} `json:"roster"`
		BotLogins []string `json:"botLogins"`
	} `json:"facts"`
}

func TestWorkItemFacts_ReturnsEveryStoredFactAndMatchesTheSchema(t *testing.T) {
	s, token := factsServer(t, []string{"silver"}, false)
	id, _ := richFactsItem(t)
	w := operatorDo(t, s, token, "GET", fmt.Sprintf("/api/v1/operator/work-items/%d/facts", id), "", nil)
	if w.Code != 200 {
		t.Fatalf("facts: %d %s", w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	for _, derived := range []string{"grade", "rarity", "tier", "kpis", "derived", "steward", "workingSeconds"} {
		if strings.Contains(w.Body.String(), `"`+derived+`"`) {
			t.Errorf("facts carry the derived %q: %s", derived, w.Body)
		}
	}
	var body factsBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	f := body.Facts
	if body.SchemaVersion != "1.0" || f.WorkItem.ExternalRef != "VIK-4242" || f.WorkItem.EstimateSeconds == nil || *f.WorkItem.EstimateSeconds != 7200 ||
		f.WorkItem.TrackerCreatedAt == nil || len(f.WorkItem.Epics) != 1 || len(f.WorkItem.Withdrawals) != 1 {
		t.Errorf("work item = %+v", f.WorkItem)
	}
	if len(f.Runs) != 1 || len(f.Shifts) != 1 || len(f.StatusTransitions) != 2 || len(f.GateTransitions) != 2 || len(f.DeployEnvironments) != 2 {
		t.Errorf("runs %d shifts %d status %d gates %d envs %d", len(f.Runs), len(f.Shifts), len(f.StatusTransitions), len(f.GateTransitions), len(f.DeployEnvironments))
	}
	if len(f.PullRequests) != 1 {
		t.Fatalf("pull requests = %d", len(f.PullRequests))
	}
	p := f.PullRequests[0]
	if p.URL != "https://forge.example/webgrip/ploeg/pulls/42" || p.MergedBy != "anna" || len(p.Reviews) != 2 || len(p.Events) != 3 ||
		len(p.Reverts) != 1 || len(p.Deployments) != 1 || p.ChangedPaths == nil || len(p.ChangedPaths.Paths) != 2 {
		t.Errorf("pull request = %+v", p)
	}
	if len(p.CIRuns) != 1 || len(p.CIRuns[0].Jobs) != 1 || p.CIRuns[0].Jobs[0].Attempt != 2 {
		t.Errorf("ci runs = %+v", p.CIRuns)
	}
	if len(p.Files) != 2 || p.Files[0].Path != "README.md" || p.Files[0].Indentation != nil ||
		p.Files[1].Indentation == nil || p.Files[1].Indentation.Added != 33 || p.Files[1].Indentation.MaxDepth != 4 {
		t.Errorf("files = %+v", p.Files)
	}
	roles := map[string]string{}
	for _, r := range f.Roster {
		roles[r.Login] = strings.Join(r.Roles, ",")
	}
	want := map[string]string{"anna": "merger", "bram": "reviewer", "Bram": "reviewer", "ploeg-bot": "author", "carla": "pusher", "dirk": "commenter", "fenna": "mover"}
	for login, r := range want {
		if roles[login] != r {
			t.Errorf("roster %s = %q, want %q (roster %v)", login, roles[login], r, f.Roster)
		}
	}
	if len(f.BotLogins) != 1 || f.BotLogins[0] != "ploeg-bot" {
		t.Errorf("bot logins = %v", f.BotLogins)
	}
	if want := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC); !f.ActivityAt.Equal(want) {
		t.Errorf("activityAt = %v, want the last gate move at %v", f.ActivityAt, want)
	}
}

func TestWorkItemFacts_StaysInTheConsumersScope(t *testing.T) {
	s, token := factsServer(t, []string{"silver"}, false)
	gold, _ := plainFactsItem(t, "gold-1", "gold", 7, "anna", time.Now().UTC())
	if w := operatorDo(t, s, token, "GET", fmt.Sprintf("/api/v1/operator/work-items/%d/facts", gold), "", nil); w.Code != 404 {
		t.Fatalf("cross-team facts: %d %s", w.Code, w.Body)
	}
	if w := operatorDo(t, s, token, "GET", "/api/v1/operator/facts?team=gold", "", nil); w.Code != 403 {
		t.Fatalf("cross-team list: %d %s", w.Code, w.Body)
	}
	w := operatorDo(t, s, token, "GET", "/api/v1/operator/facts", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "facts gold-1") {
		t.Fatalf("list leaked another team: %d %s", w.Code, w.Body)
	}
	if w := operatorDo(t, s, token, "GET", "/api/v1/operator/facts?limit=26", "", nil); w.Code != 400 {
		t.Errorf("limit 26: %d", w.Code)
	}
	if w := operatorDo(t, s, token, "GET", "/api/v1/operator/facts?before=c1.abc", "", nil); w.Code != 400 {
		t.Errorf("foreign cursor: %d", w.Code)
	}
}

type factsListBody struct {
	Facts []struct {
		WorkItem struct {
			ID string `json:"id"`
		} `json:"workItem"`
	} `json:"facts"`
	NextBefore *string `json:"nextBefore"`
}

func factsList(t *testing.T, s *Server, token, query string) factsListBody {
	t.Helper()
	w := operatorDo(t, s, token, "GET", "/api/v1/operator/facts?"+query, "", nil)
	if w.Code != 200 {
		t.Fatalf("facts list %s: %d %s", query, w.Code, w.Body)
	}
	validateOperatorSchema(t, w.Body.Bytes())
	var body factsListBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFactsList_PagesNewestActivityFirstAndFiltersByMember(t *testing.T) {
	s, token := factsServer(t, nil, false)
	now := time.Now().UTC().Truncate(time.Second)
	var want []string
	for i := 1; i <= 3; i++ {
		id, _ := plainFactsItem(t, "page-"+strconv.Itoa(i), "silver", i, "Anna", now.Add(-time.Duration(4-i)*time.Hour))
		want = append([]string{strconv.FormatInt(id, 10)}, want...)
	}
	other, _ := plainFactsItem(t, "page-other", "gold", 9, "bram", now.Add(-5*time.Hour))

	first := factsList(t, s, token, "member=anna&limit=2")
	if len(first.Facts) != 2 || first.NextBefore == nil || first.Facts[0].WorkItem.ID != want[0] || first.Facts[1].WorkItem.ID != want[1] {
		t.Fatalf("first page = %+v", first)
	}
	second := factsList(t, s, token, "member=ANNA&limit=2&before="+*first.NextBefore)
	if len(second.Facts) != 1 || second.NextBefore != nil || second.Facts[0].WorkItem.ID != want[2] {
		t.Fatalf("second page = %+v", second)
	}
	all := factsList(t, s, token, "")
	if len(all.Facts) != 4 || all.Facts[3].WorkItem.ID != strconv.FormatInt(other, 10) {
		t.Errorf("unfiltered list = %+v", all)
	}
	since := factsList(t, s, token, "since="+now.Add(-90*time.Minute).Format(time.RFC3339))
	if len(since.Facts) != 1 || since.Facts[0].WorkItem.ID != want[0] {
		t.Errorf("since = %+v", since)
	}
	if none := factsList(t, s, token, "member=nobody"); len(none.Facts) != 0 || none.NextBefore != nil {
		t.Errorf("nobody = %+v", none)
	}
	if gold := factsList(t, s, token, "team=gold"); len(gold.Facts) != 1 {
		t.Errorf("team gold = %+v", gold)
	}
}

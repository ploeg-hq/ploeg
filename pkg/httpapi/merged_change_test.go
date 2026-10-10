package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeChangeForge struct {
	mu      sync.Mutex
	pulls   map[int]string
	files   map[int][]string
	commits map[int][]string
	reads   []string
}

func (f *fakeChangeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, r.URL.Path)
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/webgrip/ploeg/pulls/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	number, tail, _ := strings.Cut(rest, "/")
	var n int
	fmt.Sscan(number, &n)
	switch tail {
	case "":
		if body, ok := f.pulls[n]; ok {
			fmt.Fprint(w, body)
			return
		}
	case "files":
		out := []map[string]string{}
		if r.URL.Query().Get("page") == "1" {
			for _, path := range f.files[n] {
				out = append(out, map[string]string{"filename": path})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	case "commits":
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, msg := range f.commits[n] {
				out = append(out, map[string]any{"commit": map[string]string{"message": msg}})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	http.NotFound(w, r)
}

func mergedPull(number int, title, body, mergedAt string, labels ...string) string {
	ls := make([]map[string]string, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, map[string]string{"name": l})
	}
	raw, _ := json.Marshal(map[string]any{"number": number, "state": "closed", "merged": true, "title": title, "body": body,
		"labels": ls, "merged_at": mergedAt, "closed_at": mergedAt, "merge_commit_sha": strings.Repeat(fmt.Sprintf("%02d", number), 20),
		"merged_by": map[string]string{"login": "stewart"}, "head": map[string]string{"sha": fmt.Sprintf("head%d", number)}})
	return string(raw)
}

func mergeEvent(number int, branch, mergedAt, by string) map[string]any {
	return map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": by},
		"pull_request": map[string]any{
			"number": number, "merged": true,
			"head":             map[string]any{"ref": branch, "sha": fmt.Sprintf("head%d", number)},
			"merge_commit_sha": strings.Repeat(fmt.Sprintf("%02d", number), 20),
			"merged_at":        mergedAt, "closed_at": mergedAt,
			"merged_by": map[string]any{"login": by},
		},
	}
}

func changeServer(t *testing.T, forge *fakeChangeForge) (*Server, string) {
	t.Helper()
	reset(t)
	api := httptest.NewServer(forge)
	t.Cleanup(api.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{
			"forgejo": &forgejo.Provider{BaseURL: api.URL, Secret: "shh", Log: slog.New(slog.DiscardHandler)},
		},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

func changeItem(t *testing.T, externalID, branch string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "Item " + externalID,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenShift(ctx, id, "silver", branch, 0); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestForgeWebhook_MergedPullRequestsRecordFilesLabelsAndReverts(t *testing.T) {
	forge := &fakeChangeForge{pulls: map[int]string{}, files: map[int][]string{}, commits: map[int][]string{}}
	s, token := changeServer(t, forge)
	h := s.Handler()
	feature := changeItem(t, "feature", "agent/vik-feature")
	merged := time.Now().Add(-20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	forge.pulls[18] = mergedPull(18, "Add the report", "", merged, "hotfix")
	forge.files[18] = []string{"pkg/store/report.go", "docs/report.md"}
	if code := forgePostEvent(t, h, "shh", "pull_request", "m-18", mergeEvent(18, "agent/vik-feature", merged, "stewart")); code != http.StatusAccepted {
		t.Fatalf("merge returned %d", code)
	}
	var stored int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM pull_request_files`).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored files = %d, %v", stored, err)
	}

	reverted := time.Now().UTC().Format(time.RFC3339)
	forge.pulls[31] = mergedPull(31, `Revert "Add the report"`, "Reverts webgrip/ploeg#18", reverted)
	forge.commits[31] = []string{"Revert \"Add the report\"\n\nThis reverts commit " + strings.Repeat("18", 20) + "."}
	if code := forgePostEvent(t, h, "shh", "pull_request", "m-31", mergeEvent(31, "revert-18", reverted, "anna")); code != http.StatusAccepted {
		t.Fatalf("revert merge returned %d", code)
	}

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", feature))
	var body struct {
		Facts struct {
			PullRequests []struct {
				Number int      `json:"number"`
				Labels []string `json:"labels"`
				Files  []struct {
					Path string `json:"path"`
				} `json:"files"`
				Reverts []struct {
					Number    int    `json:"number"`
					MatchedBy string `json:"matchedBy"`
				} `json:"reverts"`
			} `json:"pullRequests"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Facts.PullRequests) != 1 {
		t.Fatalf("facts = %s", raw)
	}
	p := body.Facts.PullRequests[0]
	if p.Number != 18 || len(p.Files) != 2 || strings.Join(p.Labels, ",") != "hotfix" {
		t.Fatalf("pull request = %+v", p)
	}
	if len(p.Reverts) != 1 || p.Reverts[0].Number != 31 || p.Reverts[0].MatchedBy != "title" {
		t.Fatalf("reverts = %+v; the revert's title names #18", p.Reverts)
	}
}

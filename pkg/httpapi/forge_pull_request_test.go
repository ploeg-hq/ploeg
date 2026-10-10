package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
)

type fakeForgejo struct {
	mu         sync.Mutex
	pull       string
	status     string
	pullReads  int
	statusAt   []string
	files      string
	filesReads int
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/pulls/18/files"):
		f.filesReads++
		if f.files == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(f.files))
	case strings.HasSuffix(r.URL.Path, "/pulls/18"):
		f.pullReads++
		_, _ = w.Write([]byte(f.pull))
	case strings.Contains(r.URL.Path, "/commits/") && strings.HasSuffix(r.URL.Path, "/status"):
		f.statusAt = append(f.statusAt, strings.TrimSuffix(strings.Split(r.URL.Path, "/commits/")[1], "/status"))
		if f.status == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(f.status))
	default:
		http.NotFound(w, r)
	}
}

func forgePostEvent(t *testing.T, h http.Handler, secret, event, delivery string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", strings.NewReader(string(b)))
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Delivery", delivery)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	req.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func pullRequestEvent(action, head string) map[string]any {
	return map[string]any{
		"action":     action,
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": "ploeg-bot"},
		"pull_request": map[string]any{"number": 18, "mergeable": true,
			"head": map[string]any{"ref": factsBranch, "sha": head}},
	}
}

func pullRequestServer(t *testing.T, forge *fakeForgejo) (*Server, string) {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(forge)
	t.Cleanup(srv.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: srv.URL, Secret: "shh",
			Log: slog.New(slog.DiscardHandler)}},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

type diffCI struct {
	additions, deletions, changedFiles *int
	ciState, ciHead                    *string
}

func readPullRequestFacts(t *testing.T) diffCI {
	t.Helper()
	var d diffCI
	if err := testPool.QueryRow(context.Background(), `SELECT additions, deletions, changed_files, ci_state, ci_head_sha
		FROM pull_requests WHERE number = 18`).Scan(&d.additions, &d.deletions, &d.changedFiles, &d.ciState, &d.ciHead); err != nil {
		t.Fatalf("pull request 18: %v", err)
	}
	return d
}

func TestForgeWebhook_OpenedPullRequestCapturesDiffAndCI(t *testing.T) {
	forge := &fakeForgejo{
		pull:   `{"state":"open","merged":false,"head":{"sha":"h1"},"additions":214,"deletions":38,"changed_files":6}`,
		status: `{"state":"pending","sha":"h1","total_count":1,"statuses":[{"context":"verify","status":"pending"}]}`,
	}
	s, _ := pullRequestServer(t, forge)
	factsItem(t)
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "open-1", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	d := readPullRequestFacts(t)
	if d.additions == nil || *d.additions != 214 || *d.deletions != 38 || *d.changedFiles != 6 ||
		d.ciState == nil || *d.ciState != "pending" || *d.ciHead != "h1" {
		t.Fatalf("captured = %+v", d)
	}

	forge.mu.Lock()
	forge.pull = `{"state":"open","merged":false,"head":{"sha":"h2"},"additions":220,"deletions":38,"changed_files":7}`
	forge.status = `{"state":"success","sha":"h2","total_count":1,"statuses":[{"context":"verify","status":"success"}]}`
	forge.mu.Unlock()
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "sync-1", pullRequestEvent("synchronized", "h2")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	d = readPullRequestFacts(t)
	if *d.additions != 220 || *d.changedFiles != 7 || *d.ciState != "success" || *d.ciHead != "h2" {
		t.Errorf("after a push = %+v", d)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if fmt.Sprint(forge.statusAt) != "[h1 h2]" {
		t.Errorf("status read at %v; want each head", forge.statusAt)
	}
}

func TestForgeWebhook_FailedCaptureKeepsTheWebhooksFacts(t *testing.T) {
	forge := &fakeForgejo{pull: `not json`}
	s, _ := pullRequestServer(t, forge)
	factsItem(t)
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "open-2", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	var state, head string
	if err := testPool.QueryRow(context.Background(), `SELECT state, head_sha FROM pull_requests WHERE number = 18`).Scan(&state, &head); err != nil {
		t.Fatal(err)
	}
	d := readPullRequestFacts(t)
	if state != "open" || head != "h1" || d.additions != nil || d.ciState != nil {
		t.Errorf("state %s head %s diff/ci %+v; failed reads must leave the figures unknown", state, head, d)
	}
}

func TestForgeWebhook_HumanPullRequestCostsNoForgeRead(t *testing.T) {
	forge := &fakeForgejo{pull: `{"state":"open","merged":false,"head":{"sha":"h1"}}`}
	s, _ := pullRequestServer(t, forge)
	factsItem(t)
	ev := pullRequestEvent("opened", "h1")
	ev["pull_request"].(map[string]any)["head"] = map[string]any{"ref": "feature/human", "sha": "h1"}
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "open-3", ev); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if forge.pullReads != 0 || len(forge.statusAt) != 0 {
		t.Errorf("reads = %d pulls, %v statuses; a pull request on no Ploeg branch is never read", forge.pullReads, forge.statusAt)
	}
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type playPaths struct {
	HeadSHA   string              `json:"headSha"`
	Truncated bool                `json:"truncated"`
	Paths     []store.ChangedPath `json:"paths"`
}

func factsPlayPaths(t *testing.T, s *Server, token string, item int64) *playPaths {
	t.Helper()
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", item))
	var body struct {
		Facts struct {
			PullRequests []struct {
				ChangedPaths *playPaths `json:"changedPaths"`
			} `json:"pullRequests"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Facts.PullRequests) != 1 {
		t.Fatalf("facts = %s; want one pull request", raw)
	}
	return body.Facts.PullRequests[0].ChangedPaths
}

func TestForgeWebhook_CapturesChangedPathsOncePerHead(t *testing.T) {
	forge := &fakeForgejo{
		pull:  `{"state":"open","merged":false,"head":{"sha":"h1"},"changed_files":2}`,
		files: `[{"filename":"AGENTS.md","status":"changed"},{"filename":".claude/settings.json","previous_filename":"settings.json","status":"renamed"}]`,
	}
	s, token := pullRequestServer(t, forge)
	item, _ := factsItem(t)
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "paths-open", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	paths := factsPlayPaths(t, s, token, item)
	want := []store.ChangedPath{{Path: "AGENTS.md", Status: "modified"},
		{Path: ".claude/settings.json", Status: "renamed", PreviousPath: "settings.json"}}
	if paths == nil || paths.HeadSHA != "h1" || fmt.Sprint(paths.Paths) != fmt.Sprint(want) || paths.Truncated {
		t.Fatalf("paths %+v; want %v at h1, not truncated", paths, want)
	}

	if code := forgePostEvent(t, h, "shh", "pull_request", "paths-again", pullRequestEvent("reopened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	forge.mu.Lock()
	reads := forge.filesReads
	forge.mu.Unlock()
	if reads != 1 {
		t.Errorf("files read %d times for one head; want once", reads)
	}

	forge.mu.Lock()
	forge.pull = `{"state":"open","merged":false,"head":{"sha":"h2"},"changed_files":3}`
	forge.files = ""
	forge.mu.Unlock()
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "paths-sync-fail", pullRequestEvent("synchronized", "h2")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	if paths := factsPlayPaths(t, s, token, item); paths == nil || paths.HeadSHA != "h1" {
		t.Fatalf("paths %+v after a failed read at a new head; want the list read at h1, never an empty one at h2", paths)
	}

	forge.mu.Lock()
	forge.files = `[{"filename":"AGENTS.md","status":"changed"},{"filename":"CLAUDE.md","status":"added"}]`
	forge.mu.Unlock()
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "paths-sync-ok", pullRequestEvent("synchronized", "h2")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	paths = factsPlayPaths(t, s, token, item)
	want = []store.ChangedPath{{Path: "AGENTS.md", Status: "modified"}, {Path: "CLAUDE.md", Status: "added"}}
	if paths == nil || paths.HeadSHA != "h2" || fmt.Sprint(paths.Paths) != fmt.Sprint(want) {
		t.Fatalf("paths %+v after a push; want %v at h2", paths, want)
	}
}

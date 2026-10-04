package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func cardPlayPaths(t *testing.T, s *Server, token string, item int64) store.CardPlay {
	t.Helper()
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Card.Plays) != 1 {
		t.Fatalf("card = %s; want one play", raw)
	}
	return body.Card.Plays[0]
}

func TestForgeWebhook_CapturesChangedPathsOncePerHead(t *testing.T) {
	forge := &fakeForgejo{
		pull:  `{"state":"open","merged":false,"head":{"sha":"h1"},"changed_files":2}`,
		files: `[{"filename":"AGENTS.md","status":"changed"},{"filename":".claude/settings.json","previous_filename":"settings.json","status":"renamed"}]`,
	}
	s, token := cardServer(t, forge)
	item, _ := factsItem(t)
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "paths-open", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	play := cardPlayPaths(t, s, token, item)
	want := []store.ChangedPath{{Path: "AGENTS.md", Status: "modified"},
		{Path: ".claude/settings.json", Status: "renamed", PreviousPath: "settings.json"}}
	if play.ChangedPaths == nil || fmt.Sprint(*play.ChangedPaths) != fmt.Sprint(want) ||
		play.ChangedPathsTruncated == nil || *play.ChangedPathsTruncated {
		t.Fatalf("paths %v truncated %v; want %v, false", play.ChangedPaths, play.ChangedPathsTruncated, want)
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
	if play := cardPlayPaths(t, s, token, item); play.ChangedPaths != nil || play.ChangedPathsTruncated != nil {
		t.Fatalf("paths %v after a failed read at a new head; want absent, never the old list or an empty one", *play.ChangedPaths)
	}

	forge.mu.Lock()
	forge.files = `[{"filename":"AGENTS.md","status":"changed"},{"filename":"CLAUDE.md","status":"added"}]`
	forge.mu.Unlock()
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "paths-sync-ok", pullRequestEvent("synchronized", "h2")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	play = cardPlayPaths(t, s, token, item)
	want = []store.ChangedPath{{Path: "AGENTS.md", Status: "modified"}, {Path: "CLAUDE.md", Status: "added"}}
	if play.ChangedPaths == nil || fmt.Sprint(*play.ChangedPaths) != fmt.Sprint(want) {
		t.Fatalf("paths %v after a push; want %v", play.ChangedPaths, want)
	}
}

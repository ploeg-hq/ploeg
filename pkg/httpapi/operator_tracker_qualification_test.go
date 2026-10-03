package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorTrackerWorkbenchQualification(t *testing.T) {
	workbench := os.Getenv("PLOEG_WORKBENCH_PATH")
	if workbench == "" {
		t.Skip("PLOEG_WORKBENCH_PATH opts into real De Vloer tracker binding qualification")
	}
	if !filepath.IsAbs(workbench) {
		t.Fatal("PLOEG_WORKBENCH_PATH must be an absolute repository path")
	}
	script := filepath.Join(workbench, "scripts", "qualify-tracker-authority.ts")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("tracker qualification script: %v", err)
	}
	f := trackerOperatorHTTPFixture(t)
	f.server.OperatorConfig.Consumers[0].Principal.Teams = []string{"delivery"}
	f.server.OperatorConfig.Teams = map[string][]string{"delivery": {"operator", "builder"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "585", Revision: f.revision, ExternalScope: "11", Team: "delivery", Title: "Canonical tracker fixture", Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}}); err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, f.itemID, "delivery", "agent/pending", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "builder", Writes: true, Cap: 2}}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(f.server.Handler())
	defer httpServer.Close()
	command := exec.CommandContext(ctx, "mise", "exec", "--", "node", script)
	command.Dir = workbench
	command.Env = append(os.Environ(), "PLOEG_QUALIFICATION_URL="+httpServer.URL, "PLOEG_QUALIFICATION_TOKEN="+f.token, "PLOEG_QUALIFICATION_TRACKER_URL="+f.api.URL+"/api/v1", "PLOEG_QUALIFICATION_WORK_ITEM_ID="+fmt.Sprint(f.itemID), "PLOEG_QUALIFICATION_PROVIDER=vikunja", "PLOEG_QUALIFICATION_TASK_ID=585", "PLOEG_QUALIFICATION_SCOPE=11", "PLOEG_QUALIFICATION_REPOSITORY_URL=https://forge.example/webgrip/example.git", "PLOEG_QUALIFICATION_TARGET_FORGE=forgejo", "PLOEG_QUALIFICATION_TARGET_OWNER=webgrip", "PLOEG_QUALIFICATION_TARGET_REPO=example", "PLOEG_QUALIFICATION_BASE_BRANCH=development", "PLOEG_QUALIFICATION_TEAM=delivery")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("De Vloer tracker qualification failed: %v\nstdout: %s\nstderr: %s", err, strings.ReplaceAll(stdout.String(), f.token, "[redacted]"), strings.ReplaceAll(stderr.String(), f.token, "[redacted]"))
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result["ok"] != true {
		t.Fatalf("qualification must confirm success in one JSON object: %v %s", err, strings.ReplaceAll(stdout.String(), f.token, "[redacted]"))
	}
	items, more, err := testStore.OperatorItems(ctx, store.OperatorFilter{Teams: []string{"delivery"}, Limit: 10})
	if err != nil || more || len(items) != 1 || items[0].ID != fmt.Sprint(f.itemID) {
		t.Fatalf("workbench created duplicate Work Item: %+v %v %v", items, more, err)
	}
	if run, err := testStore.ClaimRole(ctx, "delivery", "builder", time.Minute, 2); !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("unattended roster survived binding: %+v %v", run, err)
	}
	t.Logf("De Vloer tracker qualification: %s", strings.ReplaceAll(stdout.String(), f.token, "[redacted]"))
}

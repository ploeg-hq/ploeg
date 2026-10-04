package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

// Context bundles end to end, as far as it goes without a forge or a model:
// a person uploads through the operator API, a worker claims through the run
// API, downloads and verifies with its Run token, unpacks, and composes the
// prompt. Steering uploaded during the Run reaches the next Run only.
func TestContextBundlesEndToEnd(t *testing.T) {
	reset(t)
	s, token := contextServer(t, []string{"bronze"}, true)
	shiftID := shiftFixture(t, "9300", 10, []store.Role{{Name: "reviewer"}})
	id, _, err := testStore.TrackerWorkItemID(context.Background(), "vikunja", "9300")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	upload := "/api/v1/operator/work-items/" + strconv.FormatInt(id, 10) + "/context"
	design := zipBundle(t, map[string]string{"design/flow.md": "# Flow\n\nLogin first.\n", "mockup.txt": "[ Log in ]"})
	if code, _ := uploadContext(t, s, token, upload, "design.zip", "the agreed flow", design); code != 201 {
		t.Fatalf("upload before the Run: %d", code)
	}

	api := &worker.APIClient{Base: srv.URL, HC: srv.Client()}
	first := runOnce(t, api, "first")
	if len(first.spec.Context) != 1 || first.spec.Context[0].Name != "design.zip" {
		t.Fatalf("first Run context %+v", first.spec.Context)
	}
	for _, want := range []string{"## Context from people", "PLOEG_CONTEXT_DIR", "01-design-zip/design/flow.md", "Note: the agreed flow"} {
		if !strings.Contains(first.prompt, want) {
			t.Errorf("first prompt lacks %q", want)
		}
	}
	if got, err := os.ReadFile(filepath.Join(first.dir, "01-design-zip", "design", "flow.md")); err != nil || !strings.Contains(string(got), "Login first.") {
		t.Errorf("unpacked flow.md = %q, %v", got, err)
	}

	code, steer := uploadContext(t, s, token, upload, "steer.md", "after the demo", []byte("# Change\n\nLogout too.\n"))
	if code != 201 || steer.Context.Phase != store.ContextWhileSteering {
		t.Fatalf("steering upload: %d %+v", code, steer.Context)
	}
	if err := api.Outcome(first.claim.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(context.Background(), shiftID, 1, []store.Role{{Name: "reviewer"}}); err != nil {
		t.Fatal(err)
	}

	next := runOnce(t, api, "next")
	if len(next.spec.Context) != 2 || next.spec.Context[1].Phase != store.ContextWhileSteering {
		t.Fatalf("next Run context %+v, want the steering upload too", next.spec.Context)
	}
	if !strings.Contains(next.prompt, "## 02-steer-md: steer.md") || !strings.Contains(next.prompt, "**while steering**") {
		t.Errorf("next prompt does not flag the steering upload:\n%s", next.prompt)
	}
	if strings.Contains(first.prompt, "steer.md") {
		t.Error("the steering upload reached the Run that was already running")
	}
}

type contextRun struct {
	claim  *worker.ClaimResponse
	spec   harness.TaskSpec
	prompt string
	dir    string
}

func runOnce(t *testing.T, api *worker.APIClient, name string) contextRun {
	t.Helper()
	claim, err := api.Claim("bronze", "reviewer")
	if err != nil || claim == nil {
		t.Fatalf("%s claim: %v %v", name, claim, err)
	}
	dir := filepath.Join(t.TempDir(), "context")
	items, index, err := worker.PrepareContext(context.Background(), api, claim.RunToken, claim.Context, dir)
	if err != nil {
		t.Fatalf("%s context: %v", name, err)
	}
	spec := harness.TaskSpec{WorkItem: claim.WorkItem, Role: claim.Role, Branch: claim.Branch, TraceID: "ploeg-000000000001",
		Repo: harness.RepoRef{Owner: "webgrip", Name: "example", BaseBranch: "development"}, Context: items, ContextIndex: index}
	return contextRun{claim: claim, spec: spec, prompt: worker.ComposePrompt(spec, false, "", false), dir: dir}
}

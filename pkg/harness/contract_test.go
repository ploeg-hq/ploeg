package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The schemas in docs/contracts/ are the published contract (backlog #59);
// these tests pin the Go types to them. A failure here means either the Go
// shape or the schema changed without the other — v1 allows additive
// optional fields only (docs/contracts/README.md).

// contractsLoader resolves the schemas' canonical $id URLs
// (https://webgrip.dev/ploeg/contracts/<name>) to the local files.
type contractsLoader struct{ dir string }

func (l contractsLoader) Load(url string) (any, error) {
	f, err := os.Open(filepath.Join(l.dir, path.Base(url)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jsonschema.UnmarshalJSON(f)
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.UseLoader(contractsLoader{dir: filepath.Join("..", "..", "docs", "contracts")})
	sch, err := c.Compile("https://webgrip.dev/ploeg/contracts/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return sch
}

func validate(t *testing.T, sch *jsonschema.Schema, v any) error {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return sch.Validate(inst)
}

func fullTaskSpec() TaskSpec {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	return TaskSpec{
		WorkItem: work.WorkItem{
			ID: "42", Provider: "vikunja", ExternalID: "596", Revision: "7",
			Team: "silver", State: work.StateLeased, Origin: work.OriginAssignment,
			Priority: 3, Title: "fix the thing", Description: "<p>details</p>",
			URL: "https://vikunja.example/tasks/596", CreatedAt: now, UpdatedAt: now,
			ExternalScope: "11",
			Target:        &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
			RouteRule:     "11/silver",
			RouteHint:     "repo/ploeg",
		},
		Role:       "builder",
		Checkpoint: &work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-596"},
		Repo: RepoRef{
			Forge:    ForgeForgejo,
			ForgeURL: "http://forgejo-http.forgejo.svc.cluster.local:3000",
			Owner:    "webgrip", Name: "example", BaseBranch: "development",
		},
		Branch:  "agent/vik-596",
		TraceID: "ploeg-1cd43e1dfd6c",
		Briefing: []Finding{
			{Role: "analyst", Round: 1, Findings: "## analyst\n- the retry loop is unbounded"},
			{Role: "security", Round: 1, Findings: "## security\n- the token is logged at debug"},
		},
		OpenSpec: &OpenSpecBrief{Change: "add-widget", Root: "apps/ploeg", Source: OpenSpecSourceCLI,
			Brief: "Pending tasks:\n- 1.1 add the widget"},
	}
}

func TestTaskSpec_MatchesSchema(t *testing.T) {
	sch := compileSchema(t, "taskspec.v1.schema.json")
	if err := validate(t, sch, fullTaskSpec()); err != nil {
		t.Errorf("full TaskSpec does not validate: %v", err)
	}
}

func TestCheckpointWithInstructionFileEvidence_MatchesSchema(t *testing.T) {
	sch := compileSchema(t, "checkpoint.v1.schema.json")
	cp := work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-596", InstructionFiles: []work.InstructionFile{
		{Path: "AGENTS.md", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{Path: "pkg/CLAUDE.md", SHA256: "5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9"},
	}}
	if err := validate(t, sch, cp); err != nil {
		t.Errorf("checkpoint with instruction-file evidence does not validate: %v", err)
	}
}

func TestOutcomeReport_MatchesSchema(t *testing.T) {
	sch := compileSchema(t, "outcomereport.v1.schema.json")

	full := OutcomeReport{
		Outcome:    work.OutcomePROpened,
		Summary:    "opened a PR",
		Links:      []string{"https://forgejo.example/webgrip/example/pulls/7"},
		Checkpoint: &work.Checkpoint{Phase: "pr_opened", Branch: "agent/vik-596", PRURL: "https://forgejo.example/webgrip/example/pulls/7"},
		Usage:      &Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.42, SessionID: "sess-1"},
	}
	if err := validate(t, sch, full); err != nil {
		t.Errorf("full OutcomeReport does not validate: %v", err)
	}

	// ADR-0045: every usage figure a harness can report is on the contract.
	n := func(v int64) *int64 { return &v }
	cost := 0.41
	richUsage := OutcomeReport{
		Outcome: work.OutcomePROpened, Summary: "opened a PR",
		Usage: &Usage{
			InputTokens: 1200, OutputTokens: 9876, CostUSD: 0.58, SessionID: "sess-2",
			CacheReadInputTokens: n(812004), CacheCreationInputTokens: n(45210),
			Turns: n(37), DurationMs: n(412345), APIDurationMs: n(301200),
			ToolCalls: n(4), ToolCallsByKind: map[string]int64{"edit": 2, "read": 2},
			PeakContextTokens: n(150000), ContextWindowTokens: n(200000),
			ModelUsage: map[string]ModelUsage{"claude-sonnet-5": {
				InputTokens: n(1100), OutputTokens: n(9500), CacheReadInputTokens: n(800000),
				CacheCreationInputTokens: n(45000), CostUSD: &cost, ContextWindowTokens: n(200000),
			}},
		},
	}
	if err := validate(t, sch, richUsage); err != nil {
		t.Errorf("OutcomeReport with every usage figure does not validate: %v", err)
	}
	zeroes := OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "x",
		Usage: &Usage{ToolCalls: n(0), Turns: n(0), ToolCallsByKind: map[string]int64{}}}
	if err := validate(t, sch, zeroes); err != nil {
		t.Errorf("reported zero usage figures do not validate: %v", err)
	}

	minimal := OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "nothing to do"}
	if err := validate(t, sch, minimal); err != nil {
		t.Errorf("minimal OutcomeReport does not validate: %v", err)
	}

	// A reading Run's blackboard contribution (ADR-0011) and its verdict
	// (ADR-0017) — the one field by which an agent influences what runs next.
	for _, verdict := range []string{VerdictApprove, VerdictRequestChanges} {
		reader := OutcomeReport{
			Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed",
			Findings: "## security\n- the token is logged at debug",
			Verdict:  verdict,
		}
		if err := validate(t, sch, reader); err != nil {
			t.Errorf("OutcomeReport with verdict %q does not validate: %v", verdict, err)
		}
	}

	// A writing Run's account of its change (ADR-0042).
	writer := OutcomeReport{
		Outcome: work.OutcomePROpened, Summary: "opened a PR",
		Problem: "Refunds over €500 fail.", Solution: "- `refund.go` checks the limit first.",
	}
	if err := validate(t, sch, writer); err != nil {
		t.Errorf("OutcomeReport with a problem and solution does not validate: %v", err)
	}

	// ADR-0059: the worker's forge observation, in every state it can take.
	for _, d := range []Delivery{
		{Forge: "forgejo", Repository: "webgrip/example", Branch: "agent/vik-596", Observed: DeliveryOpened, Number: 7,
			URL: "https://forgejo.example/webgrip/example/pulls/7", Base: "main", Head: "2222222222222222222222222222222222222222"},
		{Repository: "webgrip/example", Branch: "agent/vik-596", Observed: DeliveryUpdated, Number: 7, URL: "https://forgejo.example/webgrip/example/pulls/7",
			Base: "main", Head: "2222222222222222222222222222222222222222", HeadBefore: "1111111111111111111111111111111111111111"},
		{Repository: "webgrip/example", Branch: "agent/vik-596", Observed: DeliveryNone},
		{Repository: "webgrip/example", Branch: "agent/vik-596", Observed: DeliveryUnknown, Reason: "GET /api/v1/repos/webgrip/example/pulls: HTTP 500"},
	} {
		delivered := OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "x", Delivery: &d}
		if err := validate(t, sch, delivered); err != nil {
			t.Errorf("delivery %q does not validate: %v", d.Observed, err)
		}
	}

	stuckOK := OutcomeReport{Outcome: work.OutcomeStuck, Summary: "blocked", StuckReason: "gate failed"}
	if err := validate(t, sch, stuckOK); err != nil {
		t.Errorf("stuck-with-reason does not validate: %v", err)
	}

	// failureReason is posted by the worker and stored in agent_runs; every
	// value of the taxonomy must be on the wire contract.
	for _, fr := range work.FailureReasons() {
		r := OutcomeReport{Outcome: work.OutcomeFailed, Summary: "failed", FailureReason: string(fr)}
		if err := validate(t, sch, r); err != nil {
			t.Errorf("failureReason %q does not validate: %v", fr, err)
		}
	}
}

func TestOutcomeReport_SchemaRejectsInvalid(t *testing.T) {
	sch := compileSchema(t, "outcomereport.v1.schema.json")

	// Stuck without a reason violates R4.
	stuckNoReason := OutcomeReport{Outcome: work.OutcomeStuck, Summary: "blocked"}
	if err := validate(t, sch, stuckNoReason); err == nil {
		t.Error("stuck without stuckReason validated; R4 requires the schema to reject it")
	}

	// Unknown outcome enum.
	if err := validate(t, sch, map[string]any{"outcome": "shipped", "summary": "x"}); err == nil {
		t.Error("unknown outcome enum validated")
	}

	// Unknown failure taxonomy value.
	if err := validate(t, sch, map[string]any{
		"outcome": "failed", "summary": "x", "failureReason": "vibes",
	}); err == nil {
		t.Error("unknown failureReason enum validated")
	}

	// An invented verdict. The enum is closed precisely because this field
	// decides whether more agent Runs happen (ADR-0017).
	if err := validate(t, sch, map[string]any{
		"outcome": "no_change_needed", "summary": "x", "verdict": "ship_it",
	}); err == nil {
		t.Error("unknown verdict enum validated")
	}

	for name, usage := range map[string]map[string]any{
		"negative turns":         {"turns": -1},
		"unknown usage key":      {"vibes": 3},
		"unknown modelUsage key": {"modelUsage": map[string]any{"m": map[string]any{"webSearchRequests": 0}}},
		"fractional tool calls":  {"toolCalls": 1.5},
	} {
		if err := validate(t, sch, map[string]any{"outcome": "no_change_needed", "summary": "x", "usage": usage}); err == nil {
			t.Errorf("usage with %s validated", name)
		}
	}

	for name, delivery := range map[string]map[string]any{
		"invented observation": {"repository": "o/r", "branch": "b", "observed": "merged"},
		"no repository":        {"branch": "b", "observed": "none"},
		"short head":           {"repository": "o/r", "branch": "b", "observed": "opened", "number": 1, "head": "abc123"},
		"unknown key":          {"repository": "o/r", "branch": "b", "observed": "none", "trusted": true},
	} {
		if err := validate(t, sch, map[string]any{"outcome": "no_change_needed", "summary": "x", "delivery": delivery}); err == nil {
			t.Errorf("delivery with %s validated", name)
		}
	}

	// Zero-value report ("no structured signal") is an internal sentinel,
	// never a valid wire message.
	if err := validate(t, sch, OutcomeReport{}); err == nil {
		t.Error("zero-value OutcomeReport validated; outcome enum must reject the empty string")
	}
}

func TestRunAPI_SchemaCompiles(t *testing.T) {
	sch := compileSchema(t, "run-api.v1.schema.json")
	_ = sch // compilation exercises the cross-file $refs
}

package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func contextZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func contextRef(id, name, phase string, data []byte, addedAt time.Time) harness.ContextRef {
	sum := sha256.Sum256(data)
	return harness.ContextRef{ID: id, Name: name, MediaType: "application/octet-stream", SHA256: hex.EncodeToString(sum[:]),
		Bytes: int64(len(data)), Files: 1, AddedAt: addedAt, Phase: phase}
}

// contextAPI fakes ploegd's run API: checkpoints are accepted and each
// context item is served from bodies, keyed by id.
func contextAPI(t *testing.T, bodies map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, id, ok := strings.Cut(r.URL.Path, "/context/"); ok && r.Method == http.MethodGet {
			body, found := bodies[id]
			if !found {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

var addedAt = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

func TestPrepareContext_VerifiesUnpacksAndIndexes(t *testing.T) {
	bundle := contextZip(t, map[string]string{"design/flow.md": "# Flow\n", "data.json": "{}"})
	note := []byte("# Changed my mind\n\nUse the blue button.\n")
	refs := []harness.ContextRef{
		contextRef("ctx_1", "Design Notes.zip", "before_start", bundle, addedAt),
		contextRef("ctx_2", "steer.md", "while_steering", note, addedAt.Add(time.Hour)),
	}
	refs[1].Note = "after the demo"
	api := &APIClient{Base: contextAPI(t, map[string][]byte{"ctx_1": bundle, "ctx_2": note}), HC: http.DefaultClient}
	dir := filepath.Join(t.TempDir(), "context")

	items, index, err := PrepareContext(context.Background(), api, "rt", refs, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Files != 2 || items[1].Phase != "while_steering" || items[1].Note != "after the demo" {
		t.Errorf("provenance %+v", items)
	}
	for _, rel := range []string{"index.md", "01-design-notes-zip/design/flow.md", "01-design-notes-zip/data.json", "02-steer-md/steer.md"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("context lacks %s", rel)
		}
	}
	for _, want := range []string{"## 01-design-notes-zip: Design Notes.zip", "01-design-notes-zip/design/flow.md",
		"**while steering**", "Note: after the demo", "before the work started"} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q:\n%s", want, index)
		}
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "index.md")); string(onDisk) != index {
		t.Error("index.md differs from the returned index")
	}
}

func TestPrepareContext_RefusesWhatDoesNotVerify(t *testing.T) {
	good := []byte("# notes\n")
	for name, tc := range map[string]struct {
		served []byte
		want   string
	}{
		"different bytes": {[]byte("# NOTES\n"), "sha256 is"},
		"longer body":     {[]byte("# notes\n and more"), "ploegd sent 9 bytes"},
		"missing":         {nil, "could not be downloaded"},
	} {
		t.Run(name, func(t *testing.T) {
			bodies := map[string][]byte{}
			if tc.served != nil {
				bodies["ctx_1"] = tc.served
			}
			api := &APIClient{Base: contextAPI(t, bodies), HC: http.DefaultClient}
			_, _, err := PrepareContext(context.Background(), api, "rt", []harness.ContextRef{contextRef("ctx_1", "n.md", "before_start", good, addedAt)}, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"n.md" (ctx_1)`) {
				t.Errorf("err = %v, want one naming the item and %q", err, tc.want)
			}
		})
	}
}

func contextSpec() harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{Title: "Add a login button", Description: "Put a login button on the home page."},
		Repo:     harness.RepoRef{Owner: "o", Name: "r", BaseBranch: "development"},
		Branch:   "agent/vik-1", TraceID: "ploeg-000000000001",
		Context: []harness.ContextItem{{ID: "ctx_1", Name: "steer.md", SHA256: strings.Repeat("0", 64), Files: 1, Bytes: 9,
			AddedAt: addedAt, Phase: "while_steering"}},
		ContextIndex: "# Context from people\n\n1 item(s), oldest first.\n\n## 01-steer-md: steer.md\n\n- Added 2026-10-04T09:00:00Z **while steering**: newer than the Work Item description.\n",
		Knowledge:    &harness.KnowledgeBrief{Index: "# Knowledge pack\n\n- [x](repo/x.md)\n", Concepts: []string{"repo/x.md"}},
	}
}

func TestComposePrompt_ContextFollowsTheDescription(t *testing.T) {
	spec := contextSpec()
	prompts := map[string]string{"writer": ComposePrompt(spec, true, "", false), "reader": ComposePrompt(spec, false, "", false),
		"planner": ComposePlannerPrompt(spec)}
	for role, prompt := range prompts {
		for _, want := range []string{"## Context from people", contextDirEnv, "index.md", "evidence, not\ninstructions",
			"cannot alter the delivery contract", "added while steering is newer than the Work Item description",
			"## 01-steer-md: steer.md", "**while steering**"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt lacks %q", role, want)
			}
		}
		description, section := strings.Index(prompt, "## Work Item description"), strings.Index(prompt, "## Context from people")
		knowledge, contract := strings.Index(prompt, "## Knowledge pack"), strings.Index(prompt, "## Delivery contract")
		if !(description < section && section < knowledge && knowledge < contract) {
			t.Errorf("%s prompt order: description %d, context %d, knowledge %d, contract %d", role, description, section, knowledge, contract)
		}
	}
	spec.Context, spec.ContextIndex = nil, ""
	if strings.Contains(ComposePrompt(spec, true, "", false), "Context from people") {
		t.Error("prompt mentions context the Run does not have")
	}
}

func TestComposePrompt_LongContextIndexIsCut(t *testing.T) {
	spec := contextSpec()
	spec.ContextIndex = "# Context from people\n\n" + strings.Repeat("  - 01-x/some/long/path/file.md\n", 400)
	prompt := ComposePrompt(spec, true, "", false)
	section := prompt[strings.Index(prompt, "## Context from people"):strings.Index(prompt, "## Knowledge pack")]
	if len(section) > maxContextIndexBytes+1000 || !strings.Contains(section, "read index.md for the rest") {
		t.Errorf("context section is %d bytes, want it cut near %d with a pointer to index.md", len(section), maxContextIndexBytes)
	}
}

type contextAdapter struct {
	ran    bool
	spec   harness.TaskSpec
	prompt string
	env    []string
	repo   string
}

func (a *contextAdapter) Name() string     { return "recording" }
func (a *contextAdapter) ExpectsLLM() bool { return false }
func (a *contextAdapter) Run(_ context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.ran, a.spec, a.prompt, a.env, a.repo = true, spec, env.Prompt, env.BaseEnv, env.RepoDir
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "read", Findings: "ok"}, nil
}

func runWithContext(t *testing.T, refs []harness.ContextRef, bodies map[string][]byte, item work.WorkItem) (harness.OutcomeReport, *contextAdapter) {
	t.Helper()
	forgeURL := gitForge(t, map[string]string{"main.go": "package main\n"})
	adapter := &contextAdapter{}
	w := New(Config{APIURL: contextAPI(t, bodies), ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir(), RepoKnowledgeDir: "-"},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "reviewer", PreAuthor: true, WorkItem: item, Context: refs}
	return w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", ""), adapter
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestExecute_ContextReachesTheHarnessOutsideTheClone(t *testing.T) {
	concept := "---\ntype: Decision\ntitle: Login buttons are blue\ndescription: the login button colour\ntags: [login]\n---\nThe login button is blue.\n"
	bundle := contextZip(t, map[string]string{"decisions/login-button.md": concept, "mockup.txt": "[ Log in ]"})
	refs := []harness.ContextRef{contextRef("ctx_1", "design.zip", "while_steering", bundle, addedAt)}
	item := work.WorkItem{ID: "1", ExternalID: "7", Title: "Add a login button", Description: "Put a login button on the home page."}

	report, adapter := runWithContext(t, refs, map[string][]byte{"ctx_1": bundle}, item)
	if !adapter.ran || report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("ran=%v report=%+v", adapter.ran, report)
	}
	dir := envValue(adapter.env, contextDirEnv)
	if dir == "" {
		t.Fatalf("the harness got no %s", contextDirEnv)
	}
	if rel, err := filepath.Rel(adapter.repo, dir); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("context %s is inside the clone %s, where a writer could commit it", dir, adapter.repo)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "01-design-zip", "mockup.txt")); err != nil || string(got) != "[ Log in ]" {
		t.Errorf("unpacked mockup = %q, %v", got, err)
	}
	if len(adapter.spec.Context) != 1 || adapter.spec.Context[0].ID != "ctx_1" || adapter.spec.Context[0].Files != 2 {
		t.Errorf("Task Spec context %+v", adapter.spec.Context)
	}
	if !strings.Contains(adapter.prompt, "## Context from people") || !strings.Contains(adapter.prompt, "01-design-zip/mockup.txt") {
		t.Error("the prompt does not carry the context index")
	}
	k := adapter.spec.Knowledge
	if k == nil || len(k.Concepts) != 1 || k.Concepts[0] != "context/01-design-zip/decisions/login-button.md" {
		t.Fatalf("knowledge pack %+v, want the OKF concept from the context selected", k)
	}
	if k.Sources[0].Name != contextKnowledgeSource || envValue(adapter.env, knowledgeDirEnv) == "" {
		t.Errorf("knowledge sources %+v", k.Sources)
	}
}

func TestExecute_UnverifiedContextStopsTheRun(t *testing.T) {
	data := []byte("# notes\n")
	refs := []harness.ContextRef{contextRef("ctx_1", "notes.md", "before_start", data, addedAt)}
	report, adapter := runWithContext(t, refs, map[string][]byte{"ctx_1": []byte("# tampered\n")}, work.WorkItem{ID: "1", ExternalID: "7", Title: "t"})
	if adapter.ran {
		t.Fatal("the harness ran on context that did not verify")
	}
	if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "did not verify") || !strings.Contains(report.Summary, "context") {
		t.Fatalf("report = %+v, want stuck naming the unverified context", report)
	}
}

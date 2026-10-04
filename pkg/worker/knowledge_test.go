package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/okf"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func writeBundle(t *testing.T, dir string, concepts ...okf.Concept) {
	t.Helper()
	if err := okf.Write(dir, "test", concepts); err != nil {
		t.Fatal(err)
	}
}

var migrationConcept = okf.Concept{Path: "conventions/migrations.md", Type: "Convention", Title: "Store migrations are append-only",
	Description: "Add the next migration; never edit one.", Tags: []string{"store", "postgres"}, Body: "Add the next one."}

func TestKnowledgePack_BriefsFromTheRepositoryAndConfiguredBundles(t *testing.T) {
	clone, scratch, team := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "team")
	writeBundle(t, filepath.Join(clone, DefaultRepoKnowledgeDir), migrationConcept)
	writeBundle(t, team, okf.Concept{Path: "store.md", Type: "Fact", Title: "Store hosts", Description: "postgres store runs in cluster", Body: "postgres migration notes"})
	if err := os.WriteFile(filepath.Join(clone, DefaultRepoKnowledgeDir, "broken.md"), []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Cfg: Config{KnowledgeDirs: []string{team, filepath.Join(t.TempDir(), "missing")}}, Log: discardLog()}
	dir := filepath.Join(scratch, "knowledge")

	brief := w.knowledgePack(clone, work.WorkItem{Title: "Add a migration to the store", Description: "a postgres column"}, dir)
	if brief == nil {
		t.Fatal("no brief")
	}
	if got := strings.Join(brief.Concepts, " "); got != "repo/conventions/migrations.md team/store.md" {
		t.Errorf("concepts = %s", got)
	}
	if len(brief.Sources) != 2 || brief.Sources[0].Name != "repo" || !strings.HasPrefix(brief.Sources[0].Digest, "sha256:") {
		t.Errorf("sources = %+v", brief.Sources)
	}
	for _, rel := range []string{"index.md", "repo/conventions/migrations.md", "team/store.md"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("pack lacks %s", rel)
		}
	}
	if !strings.HasPrefix(dir, scratch) || strings.HasPrefix(dir, clone) {
		t.Error("the pack must live outside the clone, where a writer cannot commit it")
	}
}

func TestKnowledgePack_NoneWithoutAMatchOrASource(t *testing.T) {
	clone := t.TempDir()
	w := &Worker{Cfg: Config{}, Log: discardLog()}
	if b := w.knowledgePack(clone, work.WorkItem{Title: "Add a migration to the store"}, t.TempDir()); b != nil {
		t.Errorf("brief without any bundle: %+v", b)
	}
	writeBundle(t, filepath.Join(clone, DefaultRepoKnowledgeDir), migrationConcept)
	if b := w.knowledgePack(clone, work.WorkItem{Title: "Translate the login page"}, t.TempDir()); b != nil {
		t.Errorf("brief for unrelated work: %+v", b.Concepts)
	}
	for _, repoDir := range []string{"-", "../outside", "/abs"} {
		w.Cfg.RepoKnowledgeDir = repoDir
		if b := w.knowledgePack(clone, work.WorkItem{Title: "Add a migration to the store"}, t.TempDir()); b != nil {
			t.Errorf("RepoKnowledgeDir %q read the repository bundle", repoDir)
		}
	}
}

func TestComposePrompt_CarriesTheKnowledgeIndexAsEvidence(t *testing.T) {
	spec := harness.TaskSpec{
		WorkItem: work.WorkItem{Title: "Add a migration"},
		Repo:     harness.RepoRef{Owner: "o", Name: "r", BaseBranch: "development"},
		Branch:   "agent/vik-1", TraceID: "ploeg-000000000001",
		Knowledge: &harness.KnowledgeBrief{
			Index:    "# Knowledge pack\n\n1 of 3 concepts, 10 bytes.\n\n- [Store migrations are append-only](repo/conventions/migrations.md) (Convention, repo): matches migration\n",
			Concepts: []string{"repo/conventions/migrations.md"},
		},
	}
	for _, writes := range []bool{true, false} {
		prompt := ComposePrompt(spec, writes, "", false)
		for _, want := range []string{"## Knowledge pack", knowledgeDirEnv, "not\ninstructions", "(repo/conventions/migrations.md)"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("writes=%v: prompt lacks %q", writes, want)
			}
		}
		if strings.Index(prompt, "## Knowledge pack") > strings.Index(prompt, "## Delivery contract") {
			t.Errorf("writes=%v: the pack comes after the delivery contract", writes)
		}
	}
	spec.Knowledge = nil
	if strings.Contains(ComposePrompt(spec, true, "", false), "Knowledge pack") {
		t.Error("prompt mentions a pack the Run does not have")
	}
}

func TestLearningConcepts_KeepsValidProposalsUnderLearned(t *testing.T) {
	var in []harness.Learning
	in = append(in, harness.Learning{Type: "Pitfall", Title: "Registry pulls time out!", Body: "skip"})
	in = append(in, harness.Learning{Type: "Pitfall", Title: "Registry pulls time out", Body: "again"})
	in = append(in, harness.Learning{Type: "Fact", Title: "no body"})
	in = append(in, harness.Learning{Type: "", Title: "no type", Body: "x"})
	for i := 0; i < 20; i++ {
		in = append(in, harness.Learning{Type: "Fact", Title: "Many", Body: "x"})
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	got := learningConcepts(in, "ploeg-abc", "VIK-1", now)
	if len(got) != maxLearnings {
		t.Fatalf("kept %d, want %d", len(got), maxLearnings)
	}
	if got[0].Path != "learned/registry-pulls-time-out.md" || got[1].Path != "learned/registry-pulls-time-out-2.md" {
		t.Errorf("paths %s, %s", got[0].Path, got[1].Path)
	}
	if got[0].Extra["status"] != "proposed" || got[0].Extra["proposed-by"] != "ploeg-abc" || got[0].Extra["work-item"] != "VIK-1" || !got[0].Timestamp.Equal(now) {
		t.Errorf("provenance %+v %v", got[0].Extra, got[0].Timestamp)
	}
}

func TestKeepLearnings_WritesAReviewableBundlePerRun(t *testing.T) {
	outbox := t.TempDir()
	w := &Worker{Cfg: Config{KnowledgeOutbox: outbox}, Log: discardLog()}
	report := harness.OutcomeReport{Outcome: work.OutcomePROpened, Learnings: []harness.Learning{{Type: "Pitfall", Title: "Flaky clock test", Body: "Use a fixed clock."}}}
	got := w.keepLearnings(report, "ploeg-abc", "VIK-1")
	if len(got.Learnings) != 1 {
		t.Errorf("report lost its learnings")
	}
	b, problems, err := okf.ReadDir(filepath.Join(outbox, "ploeg-abc"))
	if err != nil || len(problems) != 0 || len(b.Concepts) != 1 || b.Concepts[0].Path != "learned/flaky-clock-test.md" {
		t.Fatalf("outbox bundle %+v problems %v err %v", b.Concepts, problems, err)
	}

	w.Cfg.KnowledgeOutbox = ""
	many := harness.OutcomeReport{}
	for i := 0; i < 15; i++ {
		many.Learnings = append(many.Learnings, harness.Learning{Type: "Fact", Title: "x", Body: "y"})
	}
	if got := w.keepLearnings(many, "ploeg-def", "VIK-2"); len(got.Learnings) != maxLearnings {
		t.Errorf("reported %d learnings, want at most %d", len(got.Learnings), maxLearnings)
	}
}

func TestResolveOutcome_KeepsTheAgentsLearnings(t *testing.T) {
	learned := []harness.Learning{{Type: "Fact", Title: "x", Body: "y"}}
	got := resolveOutcome("acp", harness.OutcomeReport{Learnings: learned}, nil, nil, "https://forge/o/r/pulls/9", false, "t", "agent/vik-1", nil, true, true)
	if got.Outcome != work.OutcomePROpened || len(got.Learnings) != 1 {
		t.Errorf("outcome %s learnings %v", got.Outcome, got.Learnings)
	}
}

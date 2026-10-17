package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/execbin"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/okf"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestRunAgent_DropBoxLearningsReachTheReviewOutboxCapped(t *testing.T) {
	var learned []harness.Learning
	for i := 0; i < maxLearnings+2; i++ {
		learned = append(learned, harness.Learning{Type: "Fact", Title: "Fact " + string(rune('a'+i)), Body: "x"})
	}
	body, err := json.Marshal(map[string]any{"learnings": learned})
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "agent.sh")
	script := "#!/bin/sh\ncat >\"$PLOEG_OUTCOME_FILE\" <<'JSON'\n" + string(body) + "\nJSON\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	exec, err := execbin.New([]string{bin}, "")
	if err != nil {
		t.Fatal(err)
	}

	report, mintErr, runErr := runAgent(context.Background(), discardLog(), &recordingBroker{key: ""},
		harness.RunCommand(exec), testTaskSpec(), runEnv(t), llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("mintErr=%v runErr=%v", mintErr, runErr)
	}
	if len(report.Learnings) != len(learned) {
		t.Fatalf("the exec harness returned %d learnings from the drop box, want %d", len(report.Learnings), len(learned))
	}

	final := resolveOutcome("exec", report, runErr, nil, "", false, "Flaky clock", "agent/vik-2046", nil, false, true)
	if final.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("outcome %s, want no_change_needed for a writer that opened nothing", final.Outcome)
	}

	outbox := t.TempDir()
	w := &Worker{Cfg: Config{KnowledgeOutbox: outbox}, Log: discardLog()}
	kept := w.keepLearnings(final, "ploeg-2046", "VIK-2046")
	if len(kept.Learnings) != maxLearnings {
		t.Errorf("kept %d learnings, want the cap of %d applied to drop-box learnings", len(kept.Learnings), maxLearnings)
	}
	bundle, problems, err := okf.ReadDir(filepath.Join(outbox, "ploeg-2046"))
	if err != nil || len(problems) != 0 {
		t.Fatalf("outbox bundle problems %v err %v", problems, err)
	}
	if len(bundle.Concepts) != maxLearnings {
		t.Fatalf("outbox holds %d concepts, want %d", len(bundle.Concepts), maxLearnings)
	}
	for _, c := range bundle.Concepts {
		if c.Extra["status"] != "proposed" || c.Extra["proposed-by"] != "ploeg-2046" || c.Extra["work-item"] != "VIK-2046" {
			t.Errorf("concept %s provenance %+v, want proposed by ploeg-2046 for VIK-2046", c.Path, c.Extra)
		}
	}
}

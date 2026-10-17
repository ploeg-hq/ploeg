package harness

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestMergeDropBox_DeliveryClaimsNeverSurvive(t *testing.T) {
	for _, outcome := range []work.Outcome{work.OutcomePROpened, work.OutcomePRUpdated} {
		box := OutcomeReport{
			Outcome: outcome, Summary: "opened PR #123",
			Links:            []string{"https://forge/other/repo/pulls/123"},
			Checkpoint:       &work.Checkpoint{Phase: "pr_opened", Branch: "elsewhere", PRURL: "https://forge/other/repo/pulls/123"},
			FailureReason:    string(work.FailureBudget),
			Verification:     &Verification{Result: VerificationPassed},
			Delivery:         &Delivery{Repository: "other/repo", Branch: "elsewhere", Observed: DeliveryOpened, Number: 123},
			Findings:         "- reviewed",
			Verdict:          VerdictApprove,
			Problem:          "the bug",
			Solution:         "the fix",
			CreatedWorkItems: []CreatedWorkItem{createdEntry(work.CreatedSplit)},
		}
		got := MergeDropBox(OutcomeReport{}, box)
		if got.Outcome != "" || len(got.Links) != 0 || got.Checkpoint != nil || got.FailureReason != "" ||
			got.Verification != nil || got.Delivery != nil {
			t.Errorf("%s: delivery claims survived the drop box: %+v", outcome, got)
		}
		if got.Findings != box.Findings || got.Verdict != box.Verdict || got.Problem != box.Problem ||
			got.Solution != box.Solution || len(got.CreatedWorkItems) != 1 {
			t.Errorf("%s: the agent's own report was lost: %+v", outcome, got)
		}
	}

	stuck := MergeDropBox(OutcomeReport{}, OutcomeReport{Outcome: work.OutcomeStuck, Summary: "blocked", StuckReason: "no access",
		Links: []string{"https://forge/other/repo/pulls/123"}, FailureReason: string(work.FailureBudget)})
	if stuck.Outcome != work.OutcomeStuck || stuck.StuckReason != "no access" || len(stuck.Links) != 0 || stuck.FailureReason != "" {
		t.Errorf("a stuck drop box = %+v, want stuck with its reason and no links or failure reason", stuck)
	}
}

func TestMergeDropBox_LearningsSurvive(t *testing.T) {
	learned := []Learning{
		{Type: "Pitfall", Title: "Registry pulls time out", Body: "Retry with the mirror."},
		{Type: "Convention", Title: "Migrations are numbered", Body: "Take the next free number."},
	}
	got := MergeDropBox(OutcomeReport{Outcome: work.OutcomeNoChangeNeeded}, OutcomeReport{Learnings: learned})
	if len(got.Learnings) != 2 || got.Learnings[0].Title != learned[0].Title || got.Learnings[1].Title != learned[1].Title {
		t.Fatalf("the agent's learnings did not survive the drop box: %+v\n"+
			"every adapter returns through MergeDropBox, so losing them here makes ADR-0066's review channel inert", got.Learnings)
	}

	kept := MergeDropBox(OutcomeReport{Learnings: learned[:1]}, OutcomeReport{Problem: "p"})
	if len(kept.Learnings) != 1 {
		t.Errorf("an empty drop box erased the adapter's own learnings: %+v", kept.Learnings)
	}
}

package forgefacts

import (
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func intp(n int) *int { return &n }

func TestWithMissing_KeepsKnownFactsAndFillsTheRest(t *testing.T) {
	merged := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	got := WithMissing(
		provider.PullRequestFacts{State: provider.PullRequestOpen, HeadSHA: "webhook", Additions: intp(0)},
		provider.PullRequestFacts{State: provider.PullRequestMerged, HeadSHA: "read", MergedAt: &merged, MergedBy: "ryan",
			Additions: intp(9), Deletions: intp(3), ChangedFiles: intp(2)})
	if got.State != provider.PullRequestOpen || got.HeadSHA != "webhook" || *got.Additions != 0 {
		t.Errorf("known facts overwritten: %+v", got)
	}
	if got.MergedAt == nil || got.MergedBy != "ryan" || *got.Deletions != 3 || *got.ChangedFiles != 2 {
		t.Errorf("missing facts not filled: %+v", got)
	}
}

func TestFacts_ConvertsTheCommitStatus(t *testing.T) {
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	rec := Facts(PullRequest{Forge: "forgejo", Repo: "o/r", Number: 3, WorkItemID: 7},
		provider.PullRequestFacts{HeadSHA: "h", ChangedFiles: intp(4)},
		&provider.CommitStatus{SHA: "h", State: provider.CommitPending,
			Checks: []provider.CommitCheck{{Context: "verify", State: provider.CommitPending}}}, at)
	if rec.Forge != "forgejo" || rec.Number != 3 || rec.WorkItemID != 7 || *rec.ChangedFiles != 4 || rec.Additions != nil {
		t.Errorf("record = %+v", rec)
	}
	if rec.CI == nil || rec.CI.State != "pending" || rec.CI.HeadSHA != "h" || !rec.CI.CapturedAt.Equal(at) ||
		len(rec.CI.Checks) != 1 || rec.CI.Checks[0].Context != "verify" {
		t.Errorf("ci = %+v", rec.CI)
	}
	if Facts(PullRequest{}, provider.PullRequestFacts{}, nil, at).CI != nil {
		t.Error("no status read must leave CI unknown")
	}
}

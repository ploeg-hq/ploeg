package playkpi

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func atp(minutes int) *time.Time {
	t := at(minutes)
	return &t
}

func intp(n int) *int { return &n }

func notBot(login string) bool { return login != "" && !strings.EqualFold(login, "ploeg-bot") }

func seconds64(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v; want %d", name, valueOrNil(got), want)
	}
}

func valueOrNil(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestTimeline_DraftThenReadyMeasuresFromReadiness(t *testing.T) {
	p := Play{OpenedAt: atp(0), Author: "ploeg-bot", Draft: new(bool), MergedAt: atp(300), ActivityCapturedAt: atp(301),
		Commits: intp(3), FirstCommitAt: atp(-60), ForcePushes: intp(0),
		Events: []Event{
			{Kind: KindPush, Actor: "ploeg-bot", At: at(0), HeadSHA: "a"},
			{Kind: KindReady, Actor: "ploeg-bot", At: at(30)},
			{Kind: KindComment, Actor: "anna", At: at(90)},
			{Kind: KindReview, Actor: "anna", At: at(120), State: "approved", HeadSHA: "a"},
		},
		Reviews: []Review{{Reviewer: "anna", State: "approved", HeadSHA: "a", At: at(120)}}}
	tl, _ := Derive(p, notBot)
	if tl == nil || tl.ReadyAt == nil || !tl.ReadyAt.Equal(at(30)) {
		t.Fatalf("readyAt = %+v; a draft is ready when it leaves draft", tl)
	}
	seconds64(t, "toFirstFeedback", tl.ToFirstFeedback, 60*60)
	seconds64(t, "toFirstApproval", tl.ToFirstApproval, 90*60)
	seconds64(t, "approvalToMerge", tl.ApprovalToMerge, 180*60)
	seconds64(t, "openToMerge", tl.OpenToMerge, 300*60)
	seconds64(t, "codingSeconds", tl.CodingSeconds, 90*60)
	if tl.ReviewRounds != 1 || tl.Reviewers != 1 || tl.Comments == nil || *tl.Comments != 1 || *tl.Commits != 3 || *tl.ForcePushes != 0 {
		t.Errorf("counts = %+v", tl)
	}
}

func TestTimeline_NeverDraftIsReadyAtOpening(t *testing.T) {
	p := Play{OpenedAt: atp(0), ActivityCapturedAt: atp(5), Events: []Event{{Kind: KindReviewComment, Actor: "bob", At: at(15)}}}
	tl, _ := Derive(p, notBot)
	if tl.ReadyAt == nil || !tl.ReadyAt.Equal(at(0)) {
		t.Fatalf("readyAt = %v; a pull request never marked draft is ready when opened", tl.ReadyAt)
	}
	seconds64(t, "toFirstFeedback", tl.ToFirstFeedback, 15*60)
}

func TestTimeline_OpenedAsDraftAndNeverReadyHasNoReadiness(t *testing.T) {
	draft := true
	p := Play{OpenedAt: atp(0), Draft: &draft, ActivityCapturedAt: atp(5), Events: []Event{{Kind: KindComment, Actor: "bob", At: at(15)}}}
	tl, _ := Derive(p, notBot)
	if tl.ReadyAt != nil || tl.ToFirstFeedback != nil {
		t.Fatalf("timeline = %+v; a draft that never became ready has no readiness", tl)
	}
	if tl.FirstFeedbackAt == nil || !tl.FirstFeedbackAt.Equal(at(15)) {
		t.Errorf("first feedback = %v", tl.FirstFeedbackAt)
	}
}

func TestTimeline_BotsAndTheAuthorAreNotFeedback(t *testing.T) {
	p := Play{OpenedAt: atp(0), Author: "carol", ActivityCapturedAt: atp(1),
		Events: []Event{
			{Kind: KindComment, Actor: "ploeg-bot", At: at(1)},
			{Kind: KindComment, Actor: "CAROL", At: at(2)},
			{Kind: KindReviewComment, Actor: "carol", At: at(3)},
			{Kind: KindReview, Actor: "ploeg-bot", At: at(4), State: "approved"},
			{Kind: KindComment, Actor: "", At: at(5)},
			{Kind: KindComment, Actor: "dave", At: at(40)},
		},
		Reviews: []Review{{Reviewer: "ploeg-bot", State: "approved", At: at(4)}}}
	tl, _ := Derive(p, notBot)
	if tl.FirstFeedbackAt == nil || !tl.FirstFeedbackAt.Equal(at(40)) || *tl.Comments != 1 || tl.FirstApprovalAt != nil || tl.Reviewers != 0 {
		t.Fatalf("timeline = %+v; only dave's comment is feedback", tl)
	}
	if tl.ReviewRounds != 0 {
		t.Errorf("review rounds = %d; a bot review is no round", tl.ReviewRounds)
	}
}

func TestTimeline_ResponseIsTheMedianWaitForTheAuthorsNextPush(t *testing.T) {
	p := Play{OpenedAt: atp(0), Author: "ploeg-bot", ActivityCapturedAt: atp(500),
		Events: []Event{
			{Kind: KindPush, Actor: "ploeg-bot", At: at(0), HeadSHA: "a"},
			{Kind: KindReview, Actor: "anna", At: at(10), State: "changes_requested", HeadSHA: "a"},
			{Kind: KindPush, Actor: "anna", At: at(12), HeadSHA: "x"},
			{Kind: KindPush, Actor: "ploeg-bot", At: at(40), HeadSHA: "b"},
			{Kind: KindForcePush, Actor: "ploeg-bot", At: at(100), HeadSHA: "c"},
		},
		Reviews: []Review{
			{Reviewer: "anna", State: "changes_requested", HeadSHA: "a", At: at(10)},
			{Reviewer: "bob", State: "changes_requested", HeadSHA: "b", At: at(50)},
			{Reviewer: "anna", State: "approved", HeadSHA: "c", At: at(110)},
		}}
	tl, _ := Derive(p, notBot)
	seconds64(t, "responseSeconds", tl.ResponseSeconds, (30*60+50*60)/2)
	if tl.ReviewRounds != 3 || tl.Reviewers != 2 {
		t.Errorf("rounds = %d, reviewers = %d", tl.ReviewRounds, tl.Reviewers)
	}
}

func TestTimeline_WithoutActivityOnlyReviewsAreKnown(t *testing.T) {
	p := Play{OpenedAt: atp(0), MergedAt: atp(60), Reviews: []Review{{Reviewer: "anna", State: "approved", HeadSHA: "a", At: at(20)}}}
	tl, ci := Derive(p, notBot)
	if ci != nil {
		t.Errorf("ci = %+v; CI never read stays unknown", ci)
	}
	if tl.Comments != nil || tl.Commits != nil || tl.ForcePushes != nil || tl.ReadyAt != nil || tl.ResponseSeconds != nil || tl.CapturedAt != nil {
		t.Fatalf("timeline = %+v; facts only an activity read knows stay nil", tl)
	}
	if tl.FirstApprovalAt == nil || tl.ToFirstApproval != nil {
		t.Errorf("approval = %v, toFirstApproval = %v", tl.FirstApprovalAt, tl.ToFirstApproval)
	}
	seconds64(t, "openToMerge", tl.OpenToMerge, 3600)
	seconds64(t, "approvalToMerge", tl.ApprovalToMerge, 2400)
	if tl, _ := Derive(Play{}, notBot); tl != nil {
		t.Errorf("timeline of nothing = %+v", tl)
	}
}

func TestTimeline_ApprovalAfterTheMergeIsNotTheLastApproval(t *testing.T) {
	p := Play{OpenedAt: atp(0), MergedAt: atp(60), Reviews: []Review{
		{Reviewer: "anna", State: "approved", At: at(10)},
		{Reviewer: "bob", State: "approved", At: at(30)},
		{Reviewer: "carl", State: "approved", At: at(90)},
	}}
	tl, _ := Derive(p, notBot)
	if !tl.FirstApprovalAt.Equal(at(10)) || !tl.LastApprovalAt.Equal(at(30)) {
		t.Fatalf("approvals = %v, %v", tl.FirstApprovalAt, tl.LastApprovalAt)
	}
}

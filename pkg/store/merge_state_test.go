package store

import (
	"context"
	"testing"
	"time"
)

func mergeable(b bool) *bool { return &b }

func TestNextMergeCheck(t *testing.T) {
	cases := []struct {
		name      string
		prev      MergeCheck
		mergeable *bool
		head      string
		want      MergeCheck
	}{
		{"one conflicted poll stays unknown", MergeCheck{State: MergeUnknown}, mergeable(false), "a",
			MergeCheck{State: MergeUnknown, HeadSHA: "a", UnmergeablePolls: 1}},
		{"two conflicted polls on one head are conflicted", MergeCheck{State: MergeUnknown, HeadSHA: "a", UnmergeablePolls: 1}, mergeable(false), "a",
			MergeCheck{State: MergeConflicted, HeadSHA: "a", UnmergeablePolls: 2}},
		{"a conflict stays conflicted", MergeCheck{State: MergeConflicted, HeadSHA: "a", UnmergeablePolls: 2}, mergeable(false), "a",
			MergeCheck{State: MergeConflicted, HeadSHA: "a", UnmergeablePolls: 3}},
		{"a new head starts the count again", MergeCheck{State: MergeUnknown, HeadSHA: "a", UnmergeablePolls: 1}, mergeable(false), "b",
			MergeCheck{State: MergeUnknown, HeadSHA: "b", UnmergeablePolls: 1}},
		{"a new head resets a conflict to unknown", MergeCheck{State: MergeConflicted, HeadSHA: "a", UnmergeablePolls: 2}, mergeable(false), "b",
			MergeCheck{State: MergeUnknown, HeadSHA: "b", UnmergeablePolls: 1}},
		{"mergeable is clean", MergeCheck{State: MergeConflicted, HeadSHA: "a", UnmergeablePolls: 2}, mergeable(true), "a",
			MergeCheck{State: MergeClean, HeadSHA: "a"}},
		{"an uncomputed mergeability is unknown", MergeCheck{State: MergeClean, HeadSHA: "a"}, nil, "a",
			MergeCheck{State: MergeUnknown, HeadSHA: "a"}},
		{"a conflict without a head never confirms", MergeCheck{State: MergeUnknown, UnmergeablePolls: 1}, mergeable(false), "",
			MergeCheck{State: MergeUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NextMergeCheck(c.prev, c.mergeable, c.head); got != c.want {
				t.Errorf("NextMergeCheck = %+v, want %+v", got, c.want)
			}
		})
	}
}

func mergeAuditActions(t *testing.T, workItem int64) []string {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT action FROM audit_log
		WHERE work_item_id = $1 AND action LIKE 'pull_request.%' ORDER BY id`, workItem)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestRecordMergeCheck(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1598")
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 45,
		WorkItemID: item, State: "open", HeadSHA: "head-1"}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	key := PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 45}
	at := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	poll := func(m *bool, head string) MergeCheck {
		t.Helper()
		at = at.Add(time.Minute)
		check, ok, err := testStore.RecordMergeCheck(ctx, key, item, MergeObservation{Mergeable: m, HeadSHA: head, BaseBranch: "development"}, at)
		if err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v", ok, err)
		}
		return check
	}
	stored := func() (state, base string, checked time.Time) {
		t.Helper()
		if err := testStore.pool.QueryRow(ctx, `SELECT merge_state, base_branch, merge_checked_at FROM pull_requests
			WHERE number = 45`).Scan(&state, &base, &checked); err != nil {
			t.Fatal(err)
		}
		return state, base, checked
	}

	if got := poll(mergeable(false), "head-1"); got.State != MergeUnknown {
		t.Fatalf("one conflicted poll = %s, want unknown", got.State)
	}
	if actions := mergeAuditActions(t, item); len(actions) != 0 {
		t.Fatalf("an unconfirmed conflict was audited: %v", actions)
	}
	if got := poll(mergeable(false), "head-1"); got.State != MergeConflicted {
		t.Fatalf("two conflicted polls = %s, want conflicted", got.State)
	}
	if state, base, checked := stored(); state != "conflicted" || base != "development" || !checked.Equal(at) {
		t.Errorf("stored = %s %s %s", state, base, checked)
	}
	poll(mergeable(false), "head-1")
	if actions := mergeAuditActions(t, item); len(actions) != 1 || actions[0] != "pull_request.conflicted" {
		t.Fatalf("audit = %v, want one pull_request.conflicted", actions)
	}
	if got := poll(mergeable(true), "head-2"); got.State != MergeClean {
		t.Fatalf("a mergeable poll = %s, want clean", got.State)
	}
	if actions := mergeAuditActions(t, item); len(actions) != 2 || actions[1] != "pull_request.mergeable" {
		t.Fatalf("audit = %v, want pull_request.mergeable after the conflict", actions)
	}

	if _, ok, err := testStore.RecordMergeCheck(ctx, PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 46},
		item, MergeObservation{Mergeable: mergeable(false), HeadSHA: "x"}, at); err != nil || ok {
		t.Errorf("an unrecorded pull request: recorded = %v, err = %v", ok, err)
	}
	if _, ok, err := testStore.RecordMergeCheck(ctx, key, item+1000, MergeObservation{Mergeable: mergeable(false), HeadSHA: "x"}, at); err != nil || ok {
		t.Errorf("another Work Item's pull request: recorded = %v, err = %v", ok, err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestMigration0023AddsPullRequestFacts(t *testing.T) {
	ctx := context.Background()
	var applied bool
	if err := testStore.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0023_pull_request_facts.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("migration 0023 was not applied")
	}
	for table, columns := range map[string][]string{
		"pull_requests": {"forge", "repo_owner", "repo_name", "number", "work_item_id", "shift_id", "head_sha",
			"merge_commit_sha", "merged_at", "merged_by", "closed_at", "state"},
		"pull_request_reviews": {"pull_request_id", "reviewer", "state", "head_sha", "received_at"},
		"work_item_reviews":    {"state", "head_sha"},
	} {
		for _, column := range columns {
			var n int
			if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("%s.%s missing", table, column)
			}
		}
	}
}

func pullRequestItem(t *testing.T, branch string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: branch, Team: "silver", Title: branch,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "silver", branch, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id, shift
}

type storedPullRequest struct {
	workItem, shift                            int64
	state, head, mergeCommit, mergedBy, branch *string
	mergedAt, closedAt                         *time.Time
}

func readPullRequest(t *testing.T, number int) storedPullRequest {
	t.Helper()
	var p storedPullRequest
	var shift *int64
	if err := testStore.pool.QueryRow(context.Background(), `SELECT work_item_id, shift_id, state, head_sha, merge_commit_sha,
		merged_by, branch, merged_at, closed_at FROM pull_requests WHERE forge = 'forgejo' AND repo_owner = 'webgrip'
		AND repo_name = 'ploeg' AND number = $1`, number).
		Scan(&p.workItem, &shift, &p.state, &p.head, &p.mergeCommit, &p.mergedBy, &p.branch, &p.mergedAt, &p.closedAt); err != nil {
		t.Fatalf("pull request %d: %v", number, err)
	}
	if shift != nil {
		p.shift = *shift
	}
	return p
}

func TestRecordPullRequestFacts(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, shift := pullRequestItem(t, "agent/vik-1700")

	t.Run("a pull request on no Ploeg branch is not stored", func(t *testing.T) {
		ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 99,
			Branch: "feature/human", State: "merged"})
		if err != nil || ok {
			t.Fatalf("recorded = %v, err = %v; want false, nil", ok, err)
		}
		var n int
		if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM pull_requests`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("pull_requests rows = %d (%v), want 0", n, err)
		}
	})

	t.Run("a review resolves the Work Item from its branch", func(t *testing.T) {
		ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 12,
			Branch: "agent/vik-1700", HeadSHA: "head-1",
			Review: &PullRequestReview{Reviewer: "ryan", State: "changes_requested", HeadSHA: "head-1"}})
		if err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v", ok, err)
		}
		p := readPullRequest(t, 12)
		if p.workItem != item || p.shift != shift || p.head == nil || *p.head != "head-1" || p.state != nil || p.mergedAt != nil {
			t.Errorf("stored = %+v; want item %d shift %d head-1, state and merge unknown", p, item, shift)
		}
	})

	mergedAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	t.Run("a merge adds its facts and erases nothing known", func(t *testing.T) {
		ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 12,
			State: "merged", HeadSHA: "head-2", MergeCommitSHA: "merge-1", MergedAt: &mergedAt, MergedBy: "ryan"})
		if err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v; an earlier record names the Work Item", ok, err)
		}
		p := readPullRequest(t, 12)
		if p.state == nil || *p.state != "merged" || *p.head != "head-2" || *p.mergeCommit != "merge-1" ||
			*p.mergedBy != "ryan" || !p.mergedAt.Equal(mergedAt) || p.branch == nil || *p.branch != "agent/vik-1700" {
			t.Errorf("stored = %+v", p)
		}
	})

	t.Run("a merged pull request stays merged and keeps its first merge facts", func(t *testing.T) {
		later := mergedAt.Add(time.Hour)
		if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 12,
			WorkItemID: item, State: "closed", MergedAt: &later, MergedBy: "someone-else"}); err != nil {
			t.Fatal(err)
		}
		p := readPullRequest(t, 12)
		if *p.state != "merged" || !p.mergedAt.Equal(mergedAt) || *p.mergedBy != "ryan" {
			t.Errorf("stored = %+v; want the first merge facts", p)
		}
	})

	t.Run("every review is kept with its verdict", func(t *testing.T) {
		if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 12,
			Branch: "agent/vik-1700", Review: &PullRequestReview{Reviewer: "anna", State: "approved"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 12,
			Branch: "agent/vik-1700", Review: &PullRequestReview{Reviewer: "bot", State: "vibes"}}); err != nil {
			t.Fatal(err)
		}
		rows, err := testStore.pool.Query(ctx, `SELECT v.reviewer, v.state FROM pull_request_reviews v
			JOIN pull_requests p ON p.id = v.pull_request_id WHERE p.number = 12 ORDER BY v.id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var reviewer string
			var state *string
			if err := rows.Scan(&reviewer, &state); err != nil {
				t.Fatal(err)
			}
			s := "<unknown>"
			if state != nil {
				s = *state
			}
			got = append(got, reviewer+":"+s)
		}
		want := []string{"ryan:changes_requested", "anna:approved", "bot:<unknown>"}
		if len(got) != len(want) {
			t.Fatalf("reviews = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("reviews = %v, want %v", got, want)
			}
		}
	})
}

func TestRecordChangesRequestedKeepsStateAndHead(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1701")
	if _, err := testStore.RecordChangesRequested(ctx, ChangesRequested{WorkItemID: item, Provider: "forgejo",
		Repo: "webgrip/ploeg", PR: 3, Reviewer: "ryan", Body: "rename it", HeadSHA: "abc123"}); err != nil {
		t.Fatal(err)
	}
	var state string
	var head *string
	if err := testStore.pool.QueryRow(ctx, `SELECT state, head_sha FROM work_item_reviews WHERE work_item_id = $1`, item).
		Scan(&state, &head); err != nil {
		t.Fatal(err)
	}
	if state != "changes_requested" || head == nil || *head != "abc123" {
		t.Errorf("work_item_reviews state = %q head = %v", state, head)
	}
}

func TestSplitRepoKeepsNestedOwners(t *testing.T) {
	for repo, want := range map[string][2]string{
		"webgrip/ploeg":     {"webgrip", "ploeg"},
		"group/sub/project": {"group/sub", "project"},
		"no-slash":          {"", ""},
		"trailing/":         {"", ""},
		"/leading":          {"", ""},
	} {
		owner, name, _ := splitRepo(repo)
		if owner != want[0] || name != want[1] {
			t.Errorf("splitRepo(%q) = %q, %q; want %q, %q", repo, owner, name, want[0], want[1])
		}
	}
}

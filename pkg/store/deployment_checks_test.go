package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestDeployCandidates_APullRequestFirstMarkedByALaterDeployIsACandidateOfAnEarlierOne(t *testing.T) {
	f := newDeployFixture(t)
	ctx := context.Background()
	pr := f.merged(1, commit("1"), 0)
	f.merged(2, commit("2"), 5)
	late := deploy(t, "production", commit("c"), f.base.Add(3*time.Hour))
	early := deploy(t, "production", commit("a"), f.base.Add(time.Hour))
	if newly, err := testStore.RecordDeployCheck(ctx, late.ID, pr, true); err != nil || !newly {
		t.Fatalf("late mark = %v, %v", newly, err)
	}
	if got := fmt.Sprint(candidateNumbers(t, late.ID, 50)); got != "[2]" {
		t.Errorf("candidates of the marking deploy = %s; a compared pair is never offered again", got)
	}
	if got := fmt.Sprint(candidateNumbers(t, early.ID, 50)); got != "[2 1]" {
		t.Errorf("candidates of the earlier deploy = %s; it may correct the first time of 1", got)
	}
	if newly, err := testStore.RecordDeployCheck(ctx, early.ID, pr, true); err != nil || newly {
		t.Errorf("early mark = %v, %v; a correction is not a new mark", newly, err)
	}
	if newly, err := testStore.RecordDeployCheck(ctx, early.ID, pr, true); err != nil || newly {
		t.Errorf("repeated mark = %v, %v", newly, err)
	}
	var at time.Time
	if err := testStore.pool.QueryRow(ctx, `SELECT first_deployed_at FROM pull_request_deployments WHERE pull_request_id = $1`, pr).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(early.DeployedAt) {
		t.Errorf("first deploy = %v, want %v", at, early.DeployedAt)
	}
	if got := fmt.Sprint(candidateNumbers(t, late.ID, 50)); got != "[2]" {
		t.Errorf("after the correction = %s", got)
	}
	between := deploy(t, "production", commit("b"), f.base.Add(2*time.Hour))
	if got := fmt.Sprint(candidateNumbers(t, between.ID, 50)); got != "[2]" {
		t.Errorf("candidates of a deploy after the first one = %s; 1 is already deployed earlier", got)
	}
	if newly, err := testStore.RecordDeployCheck(ctx, between.ID, f.merged(3, commit("3"), 10), false); err != nil || newly {
		t.Errorf("not carried = %v, %v", newly, err)
	}
	if got := fmt.Sprint(candidateNumbers(t, between.ID, 50)); got != "[2]" {
		t.Errorf("after a negative comparison = %s; it is remembered too", got)
	}
}

func TestDeployChecks_AreClaimedDeferredAndCompleted(t *testing.T) {
	f := newDeployFixture(t)
	ctx := context.Background()
	a := deploy(t, "production", commit("a"), f.base)
	b := deploy(t, "production", commit("b"), f.base.Add(time.Hour))

	due, err := testStore.DueDeployChecks(ctx, time.Minute, 1)
	if err != nil || len(due) != 1 || due[0].ID != a.ID || due[0].SHA != commit("a") || due[0].Owner != "webgrip" ||
		!due[0].DeployedAt.Equal(f.base) {
		t.Fatalf("first claim = %+v, %v", due, err)
	}
	due, err = testStore.DueDeployChecks(ctx, time.Minute, 10)
	if err != nil || len(due) != 1 || due[0].ID != b.ID {
		t.Fatalf("second claim = %+v, %v; a claimed deployment waits for its lease", due, err)
	}
	if due, _ := testStore.DueDeployChecks(ctx, time.Minute, 10); len(due) != 0 {
		t.Errorf("third claim = %+v", due)
	}

	if gaveUp, err := testStore.DeferDeployCheck(ctx, a.ID, false, time.Minute, time.Hour, 3); err != nil || gaveUp {
		t.Fatalf("progress = %v, %v", gaveUp, err)
	}
	if due, _ := testStore.DueDeployChecks(ctx, time.Minute, 10); len(due) != 1 || due[0].ID != a.ID || due[0].Attempts != 0 {
		t.Errorf("after progress = %+v; due at once", due)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		gaveUp, err := testStore.DeferDeployCheck(ctx, a.ID, true, time.Minute, 90*time.Second, 3)
		if err != nil || gaveUp != (attempt == 3) {
			t.Fatalf("failure %d: gave up %v, %v", attempt, gaveUp, err)
		}
		var seconds float64
		var checked bool
		if err := testStore.pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM check_after - now())::float8, checked_at IS NOT NULL
			FROM deployments WHERE id = $1`, a.ID).Scan(&seconds, &checked); err != nil {
			t.Fatal(err)
		}
		wait := time.Duration(seconds * float64(time.Second))
		want := []time.Duration{time.Minute, 90 * time.Second, 90 * time.Second}[attempt-1]
		if wait < want-2*time.Second || wait > want+2*time.Second {
			t.Errorf("failure %d waits %v, want %v", attempt, wait, want)
		}
		if checked != (attempt == 3) {
			t.Errorf("failure %d: checked %v", attempt, checked)
		}
	}
	if gaveUp, err := testStore.DeferDeployCheck(ctx, a.ID, true, time.Minute, time.Hour, 3); err != nil || gaveUp {
		t.Errorf("deferring a finished check = %v, %v; it is left alone", gaveUp, err)
	}

	if err := testStore.CompleteDeployCheck(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE deployments SET check_after = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if due, _ := testStore.DueDeployChecks(ctx, time.Minute, 10); len(due) != 0 {
		t.Errorf("finished deployments claimed: %+v", due)
	}
}

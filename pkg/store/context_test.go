package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func contextUpload(workItemID int64, body string) NewWorkItemContext {
	sum := sha256.Sum256([]byte(body))
	return NewWorkItemContext{WorkItemID: workItemID, Name: "notes.md", MediaType: "text/markdown", SHA256: hex.EncodeToString(sum[:]),
		Files: 1, Content: []byte(body), Note: "what we learned", AddedBy: "ryan", Actor: "operator:workbench:ryan", MaxTotalBytes: 100}
}

func TestAddWorkItemContext_IdempotentScopedAndBounded(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)

	first, created, err := testStore.AddWorkItemContext(ctx, contextUpload(id, "# notes\n"))
	if err != nil || !created {
		t.Fatalf("first upload: created=%v err=%v", created, err)
	}
	if first.Phase != ContextBeforeStart || first.Bytes != 8 || first.AddedBy != "ryan" || len(first.ID) != 36 {
		t.Errorf("stored %+v", first)
	}
	again, created, err := testStore.AddWorkItemContext(ctx, contextUpload(id, "# notes\n"))
	if err != nil || created || again.ID != first.ID {
		t.Errorf("same bytes again: created=%v id=%s err=%v, want the first record", created, again.ID, err)
	}
	if lastAuditAction(t, id) != "context_added" {
		t.Error("the upload left no audit row")
	}

	other := contextUpload(id, "# other\n")
	other.Teams = []string{"gold"}
	if _, _, err := testStore.AddWorkItemContext(ctx, other); !errors.Is(err, ErrOperatorNotFound) {
		t.Errorf("out-of-scope upload: %v", err)
	}
	big := contextUpload(id, string(make([]byte, 93)))
	if _, _, err := testStore.AddWorkItemContext(ctx, big); !errors.Is(err, ErrContextTooLarge) {
		t.Errorf("upload past the total: %v", err)
	}

	forceItemState(t, id, "done", 0)
	if _, _, err := testStore.AddWorkItemContext(ctx, contextUpload(id, "# late\n")); !errors.Is(err, ErrContextTerminal) {
		t.Errorf("upload to a done item: %v", err)
	}

	list, err := testStore.ListWorkItemContext(ctx, id, nil, time.Time{})
	if err != nil || len(list) != 1 || list[0].ID != first.ID || list[0].Content != nil {
		t.Errorf("list = %+v, %v", list, err)
	}
	if _, err := testStore.ListWorkItemContext(ctx, id, []string{"gold"}, time.Time{}); !errors.Is(err, ErrOperatorNotFound) {
		t.Errorf("out-of-scope list: %v", err)
	}
	got, err := testStore.GetWorkItemContext(ctx, first.ID)
	if err != nil || string(got.Content) != "# notes\n" {
		t.Errorf("get = %q, %v", got.Content, err)
	}
}

func TestRunContext_OnlyWhatWasAddedBeforeTheRunStarted(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	shiftID, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-585", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	before, _, err := testStore.AddWorkItemContext(ctx, contextUpload(id, "# before\n"))
	if err != nil {
		t.Fatal(err)
	}
	if before.Phase != ContextBeforeStart {
		t.Errorf("an open Shift whose Runs have not started is not steering: %s", before.Phase)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	during, _, err := testStore.AddWorkItemContext(ctx, contextUpload(id, "# during\n"))
	if err != nil {
		t.Fatal(err)
	}
	if during.Phase != ContextWhileSteering {
		t.Errorf("upload while a Run runs: phase %s", during.Phase)
	}

	refs, err := testStore.RunContext(ctx, run.RunToken)
	if err != nil || len(refs) != 1 || refs[0].ID != before.ID {
		t.Fatalf("run context = %+v, %v; want only the item added before the Run", refs, err)
	}
	item, err := testStore.RunContextItem(ctx, run.RunToken, before.ID)
	if err != nil || string(item.Content) != "# before\n" {
		t.Errorf("run item = %q, %v", item.Content, err)
	}
	if _, err := testStore.RunContextItem(ctx, run.RunToken, during.ID); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("the running Run read steering meant for the next one: %v", err)
	}
	if _, err := testStore.RunContextItem(ctx, "not-a-run", before.ID); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("unknown run token: %v", err)
	}

	all, err := testStore.ListWorkItemContext(ctx, id, nil, time.Time{})
	if err != nil || len(all) != 2 || all[0].ID != before.ID || all[1].ID != during.ID {
		t.Errorf("list = %+v, %v; want both, oldest first", all, err)
	}
	cut, err := testStore.ListWorkItemContext(ctx, id, nil, during.AddedAt)
	if err != nil || len(cut) != 1 {
		t.Errorf("list before the steering upload = %+v, %v", cut, err)
	}
}

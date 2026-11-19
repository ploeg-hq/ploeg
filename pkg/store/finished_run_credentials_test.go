package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestFinishAskReturnsTheReadCredentialOfItsRunOnce(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _ := ingestItem(t)
	ask := admitOpenAsk(t, id, "silver")
	if err := testStore.RecordReaderForgeToken(ctx, ask.RunToken, "read-ask"); err != nil {
		t.Fatal(err)
	}
	finished, err := testStore.FinishAsk(ctx, "workbench", "alice", fmt.Sprintf("open-%d", id))
	if err != nil || finished.ForgeTokenID != "read-ask" {
		t.Fatalf("finish = %q, %v; want the Ask's read credential read-ask to revoke", finished.ForgeTokenID, err)
	}
	again, err := testStore.FinishAsk(ctx, "workbench", "alice", fmt.Sprintf("open-%d", id))
	if err != nil || again.ForgeTokenID != "" {
		t.Fatalf("a repeated finish returned %q, %v; the credential was already handed back", again.ForgeTokenID, err)
	}
}

func TestFinishChatReturnsTheReadCredentialOfItsRun(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	chat, _, err := testStore.AdmitChat(ctx, "workbench", "alice", chatInput("credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.RecordReaderForgeToken(ctx, chat.RunToken, "read-chat"); err != nil {
		t.Fatal(err)
	}
	finished, err := testStore.FinishChat(ctx, "workbench", "alice", "credential")
	if err != nil || finished.ForgeTokenID != "read-chat" {
		t.Fatalf("finish = %q, %v; want the chat's read credential read-chat to revoke", finished.ForgeTokenID, err)
	}
}

func TestClosingAnOperatorExecutionReturnsTheCredentialsOfItsRunAndLease(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e := operatorCommandFixture(t, admitOperatorFixture(t, "credentials"), "start", "start")
	if _, err := testStore.pool.Exec(ctx, `UPDATE agent_runs SET forge_token_id = 'read-execution' WHERE id = $1`, e.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE leases SET forge_token_id = 'push-execution' WHERE work_item_id = $1`, e.WorkItemID); err != nil {
		t.Fatal(err)
	}
	completed, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "complete", Action: "report", State: "completed", StopConfirmed: true, ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(completed.ForgeTokenIDs)
	if !slices.Equal(completed.ForgeTokenIDs, []string{"push-execution", "read-execution"}) {
		t.Fatalf("closing returned %v; want both credentials to revoke", completed.ForgeTokenIDs)
	}
}

func TestWithdrawalReturnsARunningReadersCredential(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	run := claimReader(t)
	if err := testStore.RecordReaderForgeToken(ctx, run.RunToken, "read-withdrawn"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if _, err := fmt.Sscan(run.Item.ID, &id); err != nil {
		t.Fatal(err)
	}
	wd, err := testStore.WithdrawWorkItem(ctx, id, nil, "operator:test", CloseReasonWithdrawnByOperator)
	if err != nil || !wd.Withdrawn {
		t.Fatalf("withdraw = %+v, %v", wd, err)
	}
	if !slices.Contains(wd.ForgeTokenIDs, "read-withdrawn") {
		t.Fatalf("withdrawal returned %v; want the reader's credential read-withdrawn to revoke", wd.ForgeTokenIDs)
	}
}

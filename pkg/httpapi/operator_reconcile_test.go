package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type failingExecutionBlockBroker struct {
	managedBrokerFixture
	failingToken string
	failure      error
	blocked      []string
}

func TestOperatorReconciliationRotatesPastFullFailingBatchAcrossRestart(t *testing.T) {
	reset(t)
	ctx := context.Background()
	broker := &failingExecutionBlockBroker{}
	control, err := NewLLMControl(testStore, broker, `[{"team":"silver","role":"operator","budgetUsd":1,"models":["trusted"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	var last store.OperatorExecution
	for i := 0; i < 101; i++ {
		input := operatorHTTPInput(fmt.Sprintf("rotation-%03d", i))
		input.Demo, input.BudgetUSD = false, 1
		e, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		e, err = testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", store.OperatorExecutionCommand{ID: "start", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		last = e
	}
	if err := control.Reserve(ctx, last.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := control.IssueOperator(ctx, last.RunToken, last.ID, last.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE operator_executions SET expires_at='2000-01-01'::timestamptz+(run_id * interval '1 second')`); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	if err := testPool.QueryRow(ctx, `SELECT expires_at FROM operator_executions WHERE id=$1`, last.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	for batch := 0; batch < 2; batch++ {
		server := &Server{Store: testStore, LLMControl: control}
		if err := server.ReconcileOperatorExecutions(ctx); err == nil {
			t.Fatal("accountless expired executions did not retain unresolved cleanup")
		}
		account, err := testStore.LLMAccount(ctx, last.RunToken)
		if err != nil {
			t.Fatal(err)
		}
		expected := "issued"
		if batch == 1 {
			expected = "blocked"
		}
		if account.State != expected {
			t.Fatalf("batch %d did not rotate fairly: got%s want%s", batch, account.State, expected)
		}
	}
	var attempted int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM operator_executions WHERE last_block_attempt_at IS NOT NULL`).Scan(&attempted); err != nil || attempted != 101 {
		t.Fatalf("bounded batches did not visit every expired execution: %d %v", attempted, err)
	}
	execution, err := testStore.OperatorExecution(ctx, last.ID, "workbench", "alice")
	if err != nil || !execution.ExpiresAt.Equal(deadline) || execution.State != "interrupted" || execution.StopConfirmed {
		t.Fatalf("cleanup rotation changed execution authority: %+v %v", execution, err)
	}
	if len(broker.blocked) != 1 || broker.blocked[0] != last.RunToken {
		t.Fatal("later credential was not blocked exactly once")
	}
}

func (b *failingExecutionBlockBroker) RevokeForRun(_ context.Context, token string) error {
	b.blocked = append(b.blocked, token)
	if token == b.failingToken {
		return b.failure
	}
	return nil
}

func TestOperatorReconciliationContinuesAfterEarlierBlockFailure(t *testing.T) {
	for _, accountless := range []bool{false, true} {
		name := "unresolved-gateway-alias"
		if accountless {
			name = "admission-without-account"
		}
		t.Run(name, func(t *testing.T) {
			reset(t)
			ctx := context.Background()
			broker := &failingExecutionBlockBroker{failure: errors.New("unresolved gateway alias")}
			control, err := NewLLMControl(testStore, broker, `[{"team":"silver","role":"operator","budgetUsd":1,"models":["trusted"],"ttl":"1h"}]`)
			if err != nil {
				t.Fatal(err)
			}
			executions := make([]store.OperatorExecution, 2)
			for i, session := range []string{"expired-first", "expired-later"} {
				input := operatorHTTPInput(session)
				input.Demo, input.BudgetUSD = false, 1
				e, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				e, err = testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", store.OperatorExecutionCommand{ID: "start", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if i != 0 || !accountless {
					if err := control.Reserve(ctx, e.RunToken); err != nil {
						t.Fatal(err)
					}
					if _, err := control.IssueOperator(ctx, e.RunToken, e.ID, e.Generation); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := testPool.Exec(ctx, `UPDATE operator_executions SET expires_at=now()-($2::int * interval '1 second') WHERE id=$1`, e.ID, 2-i); err != nil {
					t.Fatal(err)
				}
				executions[i] = e
			}
			broker.failingToken = executions[0].RunToken
			server := &Server{Store: testStore, LLMControl: control}
			for attempt := 0; attempt < 2; attempt++ {
				err := server.ReconcileOperatorExecutions(ctx)
				if err == nil || (!accountless && !errors.Is(err, broker.failure)) {
					t.Fatalf("unresolved block was not reported: %v", err)
				}
				later, err := testStore.LLMAccount(ctx, executions[1].RunToken)
				if err != nil || later.State != "blocked" {
					t.Fatalf("earlier failure starved later cleanup: state=%s err=%v", later.State, err)
				}
			}
			laterBlocks := 0
			for _, token := range broker.blocked {
				if token == executions[1].RunToken {
					laterBlocks++
				}
			}
			if laterBlocks != 1 {
				t.Fatalf("later account block was not idempotent: %d attempts", laterBlocks)
			}
		})
	}
}

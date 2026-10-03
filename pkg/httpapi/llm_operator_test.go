package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type pausedMintBroker struct {
	managedBrokerFixture
	onMint  func()
	revoked int
}

func (b *pausedMintBroker) Mint(ctx context.Context, req llmbroker.MintRequest) (llmbroker.Credential, error) {
	credential, err := b.managedBrokerFixture.Mint(ctx, req)
	b.onMint()
	return credential, err
}

func (b *pausedMintBroker) Revoke(context.Context, llmbroker.Credential) error {
	b.revoked++
	return nil
}

func TestOperatorMintDoesNotReturnCredentialAfterConcurrentPause(t *testing.T) {
	reset(t)
	ctx := context.Background()
	input := operatorHTTPInput("credential-race")
	input.Demo = false
	input.BudgetUSD = 1
	e, _, err := testStore.AdmitOperatorExecution(ctx, "workbench", "alice", input, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e, err = testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", store.OperatorExecutionCommand{ID: "start", Action: "start", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b := &pausedMintBroker{}
	b.onMint = func() {
		if _, err := testStore.CommandOperatorExecution(ctx, e.ID, "workbench", "alice", store.OperatorExecutionCommand{ID: "pause", Action: "pause", ExpectedRevision: e.Revision, Generation: e.Generation}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	control, err := NewLLMControl(testStore, b, `[{"team":"silver","role":"operator","budgetUsd":2,"models":["trusted"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Reserve(ctx, e.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Issue(ctx, e.RunToken); err == nil {
		t.Fatal("operator issuance bypassed execution generation")
	}
	if _, err := control.IssueOperator(ctx, e.RunToken, e.ID, e.Generation+1); err == nil {
		t.Fatal("stale execution generation minted a credential")
	}
	credential, err := control.IssueOperator(ctx, e.RunToken, e.ID, e.Generation)
	if err == nil || credential.APIKey != "" || b.calls != 1 || b.revoked != 1 {
		t.Fatalf("pause race returned authority: calls=%d revoked=%d err=%v", b.calls, b.revoked, err)
	}
	account, err := testStore.LLMAccount(ctx, e.RunToken)
	if err != nil || account.State != "unknown" || account.Authorized != 1 {
		t.Fatalf("unresolved issuance lost hold: %+v %v", account, err)
	}
}

func TestBlockingUnissuedAccountIsIdempotentAndPreventsLaterMint(t *testing.T) {
	b := &managedBrokerFixture{}
	s, bootstrap := secureWorkerFixture(t, b)
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken string `json:"runToken"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &claim) != nil {
		t.Fatal("claim failed")
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.LLMControl.Block(ctx, claim.RunToken); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.LLMControl.Issue(ctx, claim.RunToken); err == nil || b.calls != 0 {
		t.Fatal("blocked reservation minted a credential")
	}
	a, err := testStore.LLMAccount(ctx, claim.RunToken)
	if err != nil || a.State != "blocked" || a.Authorized != 1 || a.ObservedSpend == nil || *a.ObservedSpend != 0 {
		t.Fatalf("unissued account lost reservation: %+v %v", a, err)
	}
}

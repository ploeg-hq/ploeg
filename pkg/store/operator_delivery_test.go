package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func deliveryFixture(t *testing.T) (OperatorExecution, DeliveryAccess, DeliveryPolicy, AdmitDeliveryCandidate) {
	t.Helper()
	in := operatorExecutionInput("delivery-session")
	in.Demo = false
	e, _, err := testStore.AdmitOperatorExecution(context.Background(), "workbench", "alice", in, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e = operatorCommandFixture(t, e, "start-delivery", "start")
	e, err = testStore.CommandOperatorExecution(context.Background(), e.ID, "workbench", "alice", OperatorExecutionCommand{ID: "complete-delivery", Action: "report", ExpectedRevision: e.Revision, Generation: e.Generation, State: "completed", StopConfirmed: true}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := DeliveryAccess{Consumer: "verifier", Actor: "alice", Verifier: true}
	p := DeliveryPolicy{RepositoryID: in.RepositoryID, PolicySHA256: strings.Repeat("a", 64), VerifierID: "de-vloer-docker-v1", MinTests: 2, PublicationEnabled: true}
	candidate := AdmitDeliveryCandidate{Generation: e.Generation, RepositoryID: in.RepositoryID, RepositoryURL: in.RepositoryURL, BaseSHA: strings.Repeat("b", 40), CanonicalSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40), ArtifactSHA256: strings.Repeat("e", 64), PolicySHA256: p.PolicySHA256}
	return e, a, p, candidate
}

func deliveryReceiptInput(c DeliveryCandidate, p DeliveryPolicy) AdmitVerificationReceipt {
	return AdmitVerificationReceipt{CandidateID: c.ID, PolicySHA256: p.PolicySHA256, CanonicalSHA: c.CanonicalSHA, TreeSHA: c.TreeSHA, ArtifactSHA256: c.ArtifactSHA256, Passed: true, TestCount: p.MinTests, VerifierID: p.VerifierID, EvidenceSHA256: strings.Repeat("f", 64)}
}

func approvedDeliveryFixture(t *testing.T) (OperatorExecution, DeliveryAccess, DeliveryPolicy, DeliveryCandidate, VerificationReceipt, DeliveryApproval) {
	t.Helper()
	e, a, p, in := deliveryFixture(t)
	c, _, err := testStore.AdmitDeliveryCandidate(context.Background(), e.ID, a, p, in)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := testStore.AdmitVerificationReceipt(context.Background(), e.ID, a, p, deliveryReceiptInput(c, p))
	if err != nil {
		t.Fatal(err)
	}
	owner := DeliveryAccess{Consumer: "workbench", Actor: "alice", AuthenticatedBy: "reviewer"}
	approval, _, err := testStore.ApproveDeliveryCandidate(context.Background(), e.ID, owner, p, ApproveDeliveryCandidate{CandidateID: c.ID, ReceiptID: receipt.ID, PolicySHA256: p.PolicySHA256})
	if err != nil {
		t.Fatal(err)
	}
	return e, a, p, c, receipt, approval
}

func TestDeliveryCandidateIsImmutableAndBoundToStoppedRegisteredExecution(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e, a, p, in := deliveryFixture(t)
	for _, alter := range []func(*AdmitDeliveryCandidate){
		func(v *AdmitDeliveryCandidate) { v.Generation++ },
		func(v *AdmitDeliveryCandidate) { v.RepositoryID = "other" },
		func(v *AdmitDeliveryCandidate) { v.RepositoryURL = "https://forge.example/other/repo.git" },
		func(v *AdmitDeliveryCandidate) { v.PolicySHA256 = strings.Repeat("1", 64) },
	} {
		changed := in
		alter(&changed)
		if _, _, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, changed); !errors.Is(err, ErrExecutionConflict) {
			t.Fatalf("binding changed: %v", err)
		}
	}
	worker := a
	worker.Verifier = false
	if _, _, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, worker, p, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("untrusted candidate: %v", err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET stop_confirmed=false WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("writer still active: %v", err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET stop_confirmed=true WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	c, created, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, in)
	if err != nil || !created {
		t.Fatalf("candidate: %v %v", created, err)
	}
	replay, created, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, in)
	if err != nil || created || replay.ID != c.ID {
		t.Fatalf("replay: %+v %v %v", replay, created, err)
	}
	in.TreeSHA = strings.Repeat("1", 40)
	if _, _, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("candidate overwritten: %v", err)
	}
	other := a
	other.Actor = "bob"
	if _, err := testStore.OperatorDelivery(ctx, e.ID, other); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("cross actor: %v", err)
	}
	other = a
	other.Verifier = false
	if _, err := testStore.OperatorDelivery(ctx, e.ID, other); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("other consumer gained access: %v", err)
	}
	if c.WorkItemID != e.WorkItemID || c.BaseBranch != "development" || c.RecordedBy != "verifier" {
		t.Fatalf("incomplete binding: %+v", c)
	}
}

func TestVerificationReceiptCannotBlessWrongEvidenceOrBypassPolicy(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e, a, p, in := deliveryFixture(t)
	c, _, err := testStore.AdmitDeliveryCandidate(ctx, e.ID, a, p, in)
	if err != nil {
		t.Fatal(err)
	}
	input := deliveryReceiptInput(c, p)
	for _, alter := range []func(*AdmitVerificationReceipt){
		func(v *AdmitVerificationReceipt) { v.CandidateID = "other" },
		func(v *AdmitVerificationReceipt) { v.PolicySHA256 = strings.Repeat("1", 64) },
		func(v *AdmitVerificationReceipt) { v.CanonicalSHA = strings.Repeat("1", 40) },
		func(v *AdmitVerificationReceipt) { v.TreeSHA = strings.Repeat("1", 40) },
		func(v *AdmitVerificationReceipt) { v.ArtifactSHA256 = strings.Repeat("1", 64) },
		func(v *AdmitVerificationReceipt) { v.VerifierID = "worker" },
		func(v *AdmitVerificationReceipt) { v.TestCount = 0 },
	} {
		changed := input
		alter(&changed)
		if _, _, err := testStore.AdmitVerificationReceipt(ctx, e.ID, a, p, changed); !errors.Is(err, ErrExecutionConflict) {
			t.Fatalf("unbound receipt: %v", err)
		}
	}
	untrusted := DeliveryAccess{Consumer: "workbench", Actor: "alice"}
	if _, _, err := testStore.AdmitVerificationReceipt(ctx, e.ID, untrusted, p, input); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("worker self verification: %v", err)
	}
	input.Passed = false
	input.TestCount = 0
	receipt, created, err := testStore.AdmitVerificationReceipt(ctx, e.ID, a, p, input)
	if err != nil || !created {
		t.Fatalf("failed evidence lost: %v", err)
	}
	replay, created, err := testStore.AdmitVerificationReceipt(ctx, e.ID, a, p, input)
	if err != nil || created || replay.ID != receipt.ID {
		t.Fatalf("failed replay: %+v %v", replay, err)
	}
	if _, _, err := testStore.ApproveDeliveryCandidate(ctx, e.ID, untrusted, p, ApproveDeliveryCandidate{CandidateID: c.ID, ReceiptID: receipt.ID, PolicySHA256: p.PolicySHA256}); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("failed verification approved: %v", err)
	}
	input.Passed = true
	input.TestCount = 2
	if _, _, err := testStore.AdmitVerificationReceipt(ctx, e.ID, a, p, input); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("failed receipt replaced: %v", err)
	}
}

func TestPublicationBarrierGrantsOneEffectAndNeverRetriesUnknown(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e, verifier, p, c, receipt, approval := approvedDeliveryFixture(t)
	owner := DeliveryAccess{Consumer: "workbench", Actor: "alice"}
	in := ReservePublication{OperationID: "publish-one", CandidateID: c.ID, ReceiptID: receipt.ID, ApprovalID: approval.ID, PolicySHA256: p.PolicySHA256, Branch: "vloer/publish-one"}
	var wg sync.WaitGroup
	grants := make(chan bool, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, granted, err := testStore.ReservePublication(ctx, e.ID, owner, p, in)
			grants <- granted
			errs <- err
		}()
	}
	wg.Wait()
	close(grants)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for granted := range grants {
		if granted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("external effect authorized %d times", count)
	}
	changed := in
	changed.OperationID = "publish-two"
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, p, changed); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("replacement operation: %v", err)
	}
	status := PublicationStatus{State: "unknown", CanonicalSHA: c.CanonicalSHA, Branch: in.Branch}
	if _, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, verifier, status); err != nil {
		t.Fatal(err)
	}
	replay, granted, err := testStore.ReservePublication(ctx, e.ID, owner, p, in)
	if err != nil || granted || replay.State != "unknown" {
		t.Fatalf("unknown retry: %+v %v %v", replay, granted, err)
	}
	status.State = "not_found"
	if _, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, verifier, status); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("negative lookup cleared barrier: %v", err)
	}
	status.State = "published"
	status.RemoteID = "42"
	status.RemoteURL = "https://forge.example/webgrip/example/pulls/42"
	status.CanonicalSHA = strings.Repeat("1", 40)
	if _, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, verifier, status); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("wrong canonical publication: %v", err)
	}
	status.CanonicalSHA = c.CanonicalSHA
	if _, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, owner, status); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("untrusted status: %v", err)
	}
	published, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, verifier, status)
	if err != nil || published.State != "published" {
		t.Fatalf("positive reconciliation: %+v %v", published, err)
	}
	status.State = "unknown"
	status.RemoteID = ""
	status.RemoteURL = ""
	if _, err := testStore.RecordPublicationStatus(ctx, e.ID, in.OperationID, verifier, status); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("published state regressed: %v", err)
	}
	var rows int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM operator_publication_operations`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("operation count: %d %v", rows, err)
	}
}

func TestPublicationRequiresCurrentPolicyExactApprovalAndExplicitEnablement(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	e, _, p, c, receipt, approval := approvedDeliveryFixture(t)
	owner := DeliveryAccess{Consumer: "workbench", Actor: "alice"}
	in := ReservePublication{OperationID: "publish-one", CandidateID: c.ID, ReceiptID: receipt.ID, ApprovalID: approval.ID, PolicySHA256: p.PolicySHA256, Branch: "vloer/publish-one"}
	disabled := p
	disabled.PublicationEnabled = false
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, disabled, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("publication default ignored: %v", err)
	}
	current := p
	current.PolicySHA256 = strings.Repeat("1", 64)
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, current, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("policy drift ignored: %v", err)
	}
	current = p
	current.MinTests++
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, current, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("new required check ignored: %v", err)
	}
	protected := in
	protected.Branch = c.BaseBranch
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, p, protected); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("publication authorized protected base: %v", err)
	}
	changed := in
	changed.ApprovalID = "other"
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, p, changed); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("approval mismatch ignored: %v", err)
	}
	if approval.Actor != "reviewer" {
		t.Fatalf("acting human lost: %+v", approval)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE operator_executions SET demo=true WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.ReservePublication(ctx, e.ID, owner, p, in); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("demo published: %v", err)
	}
}

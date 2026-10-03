package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type DeliveryAccess struct {
	Consumer        string
	Actor           string
	AuthenticatedBy string
	Verifier        bool
}

type DeliveryPolicy struct {
	RepositoryID       string `json:"repositoryId"`
	PolicySHA256       string `json:"policySha256"`
	VerifierID         string `json:"verifierId"`
	MinTests           int    `json:"minTests"`
	PublicationEnabled bool   `json:"publicationEnabled"`
}

type AdmitDeliveryCandidate struct {
	Generation     int64  `json:"generation"`
	RepositoryID   string `json:"repositoryId"`
	RepositoryURL  string `json:"repositoryUrl"`
	BaseSHA        string `json:"baseSha"`
	CanonicalSHA   string `json:"canonicalSha"`
	TreeSHA        string `json:"treeSha"`
	ArtifactSHA256 string `json:"artifactSha256"`
	PolicySHA256   string `json:"policySha256"`
}

type DeliveryCandidate struct {
	ID          string `json:"id"`
	ExecutionID string `json:"executionId"`
	WorkItemID  string `json:"workItemId"`
	BaseBranch  string `json:"baseBranch"`
	AdmitDeliveryCandidate
	RecordedBy string    `json:"recordedBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

type AdmitVerificationReceipt struct {
	CandidateID    string `json:"candidateId"`
	PolicySHA256   string `json:"policySha256"`
	CanonicalSHA   string `json:"canonicalSha"`
	TreeSHA        string `json:"treeSha"`
	ArtifactSHA256 string `json:"artifactSha256"`
	Passed         bool   `json:"passed"`
	TestCount      int    `json:"testCount"`
	VerifierID     string `json:"verifierId"`
	EvidenceSHA256 string `json:"evidenceSha256"`
}

type VerificationReceipt struct {
	ID string `json:"id"`
	AdmitVerificationReceipt
	RecordedBy string    `json:"recordedBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ApproveDeliveryCandidate struct {
	CandidateID  string `json:"candidateId"`
	ReceiptID    string `json:"receiptId"`
	PolicySHA256 string `json:"policySha256"`
}

type DeliveryApproval struct {
	ID string `json:"id"`
	ApproveDeliveryCandidate
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"createdAt"`
}

type ReservePublication struct {
	OperationID  string `json:"operationId"`
	CandidateID  string `json:"candidateId"`
	ReceiptID    string `json:"receiptId"`
	ApprovalID   string `json:"approvalId"`
	PolicySHA256 string `json:"policySha256"`
	Branch       string `json:"branch"`
}

type PublicationOperation struct {
	ID          string `json:"id"`
	ExecutionID string `json:"executionId"`
	ReservePublication
	State         string    `json:"state"`
	CanonicalSHA  string    `json:"canonicalSha"`
	RepositoryURL string    `json:"repositoryUrl"`
	BaseBranch    string    `json:"baseBranch"`
	RemoteID      string    `json:"remoteId,omitempty"`
	RemoteURL     string    `json:"remoteUrl,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type PublicationStatus struct {
	State        string `json:"state"`
	RemoteID     string `json:"remoteId,omitempty"`
	RemoteURL    string `json:"remoteUrl,omitempty"`
	CanonicalSHA string `json:"canonicalSha"`
	Branch       string `json:"branch"`
}

type OperatorDelivery struct {
	Candidate *DeliveryCandidate    `json:"candidate"`
	Receipt   *VerificationReceipt  `json:"receipt"`
	Approval  *DeliveryApproval     `json:"approval"`
	Operation *PublicationOperation `json:"operation"`
}

func (s *Store) OperatorDeliveryExecution(ctx context.Context, id string, a DeliveryAccess) (OperatorExecution, error) {
	return scanOperatorExecution(s.pool.QueryRow(ctx, executionSelect+`WHERE e.id=$1 AND (e.consumer=$2 OR $4) AND e.actor=$3`, id, a.Consumer, a.Actor, a.Verifier))
}

func lockDelivery(ctx context.Context, tx pgx.Tx, id string, a DeliveryAccess) (OperatorExecution, error) {
	return scanOperatorExecution(tx.QueryRow(ctx, executionSelect+`WHERE e.id=$1 AND (e.consumer=$2 OR $4) AND e.actor=$3 FOR UPDATE OF e`, id, a.Consumer, a.Actor, a.Verifier))
}

func readDelivery(ctx context.Context, tx pgx.Tx, id string) (OperatorDelivery, error) {
	var d OperatorDelivery
	var c, r, a, o []byte
	err := tx.QueryRow(ctx, `SELECT c.data,r.data,a.data,o.data FROM operator_executions e
 LEFT JOIN operator_delivery_candidates c ON c.execution_id=e.id
 LEFT JOIN operator_delivery_receipts r ON r.candidate_id=c.id
 LEFT JOIN operator_delivery_approvals a ON a.candidate_id=c.id
 LEFT JOIN operator_publication_operations o ON o.execution_id=e.id WHERE e.id=$1`, id).Scan(&c, &r, &a, &o)
	if err != nil {
		return d, err
	}
	if c != nil {
		if err = json.Unmarshal(c, &d.Candidate); err != nil {
			return d, err
		}
	}
	if r != nil {
		if err = json.Unmarshal(r, &d.Receipt); err != nil {
			return d, err
		}
	}
	if a != nil {
		if err = json.Unmarshal(a, &d.Approval); err != nil {
			return d, err
		}
	}
	if o != nil {
		if err = json.Unmarshal(o, &d.Operation); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (s *Store) OperatorDelivery(ctx context.Context, id string, a DeliveryAccess) (OperatorDelivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorDelivery{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockDelivery(ctx, tx, id, a); err != nil {
		return OperatorDelivery{}, err
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

func deliveryAudit(ctx context.Context, tx pgx.Tx, e OperatorExecution, a DeliveryAccess, kind string, value any) error {
	item, err := strconv.ParseInt(e.WorkItemID, 10, 64)
	if err != nil {
		return err
	}
	actor := a.AuthenticatedBy
	if actor == "" {
		actor = a.Actor
	}
	return audit(ctx, tx, "operator:"+a.Consumer+":"+actor, kind, &item, value)
}

func deliveryPolicyMatches(c *DeliveryCandidate, p DeliveryPolicy) bool {
	return c != nil && c.RepositoryID == p.RepositoryID && c.PolicySHA256 == p.PolicySHA256 && p.MinTests > 0 && p.VerifierID != ""
}

func (s *Store) AdmitDeliveryCandidate(ctx context.Context, id string, a DeliveryAccess, p DeliveryPolicy, in AdmitDeliveryCandidate) (DeliveryCandidate, bool, error) {
	var out DeliveryCandidate
	if !a.Verifier {
		return out, false, ErrExecutionConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	e, err := lockDelivery(ctx, tx, id, a)
	if err != nil {
		return out, false, err
	}
	if e.State != "completed" || !e.StopConfirmed || e.Generation != in.Generation {
		return out, false, ErrExecutionConflict
	}
	var repo, url, base string
	if err = tx.QueryRow(ctx, `SELECT repository_id,repository_url,base_branch FROM operator_executions WHERE id=$1`, id).Scan(&repo, &url, &base); err != nil {
		return out, false, err
	}
	if repo == "" || base == "" || repo != in.RepositoryID || url != in.RepositoryURL || repo != p.RepositoryID || in.PolicySHA256 != p.PolicySHA256 || p.MinTests < 1 || p.VerifierID == "" {
		return out, false, ErrExecutionConflict
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return out, false, err
	}
	fingerprint := executionFingerprint(in)
	if d.Candidate != nil {
		if executionFingerprint(d.Candidate.AdmitDeliveryCandidate) != fingerprint {
			return out, false, ErrExecutionConflict
		}
		return *d.Candidate, false, tx.Commit(ctx)
	}
	out = DeliveryCandidate{ID: executionFingerprint([]string{id, fingerprint})[:32], ExecutionID: id, WorkItemID: e.WorkItemID, BaseBranch: base, AdmitDeliveryCandidate: in, RecordedBy: a.Consumer, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, `INSERT INTO operator_delivery_candidates(id,execution_id,work_item_id,fingerprint,data) VALUES($1,$2,$3,$4,$5)`, out.ID, id, e.WorkItemID, fingerprint, data); err != nil {
		return out, false, err
	}
	if err = deliveryAudit(ctx, tx, e, a, "delivery.candidate_admitted", out); err != nil {
		return out, false, err
	}
	return out, true, tx.Commit(ctx)
}

func (s *Store) AdmitVerificationReceipt(ctx context.Context, id string, a DeliveryAccess, p DeliveryPolicy, in AdmitVerificationReceipt) (VerificationReceipt, bool, error) {
	var out VerificationReceipt
	if !a.Verifier {
		return out, false, ErrExecutionConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	e, err := lockDelivery(ctx, tx, id, a)
	if err != nil {
		return out, false, err
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return out, false, err
	}
	c := d.Candidate
	if !deliveryPolicyMatches(c, p) || c.ID != in.CandidateID || in.PolicySHA256 != p.PolicySHA256 || in.VerifierID != p.VerifierID || in.CanonicalSHA != c.CanonicalSHA || in.TreeSHA != c.TreeSHA || in.ArtifactSHA256 != c.ArtifactSHA256 || in.TestCount < 0 || (in.Passed && in.TestCount < p.MinTests) {
		return out, false, ErrExecutionConflict
	}
	fingerprint := executionFingerprint(in)
	if d.Receipt != nil {
		if executionFingerprint(d.Receipt.AdmitVerificationReceipt) != fingerprint {
			return out, false, ErrExecutionConflict
		}
		return *d.Receipt, false, tx.Commit(ctx)
	}
	out = VerificationReceipt{ID: executionFingerprint([]string{c.ID, fingerprint})[:32], AdmitVerificationReceipt: in, RecordedBy: a.Consumer, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, `INSERT INTO operator_delivery_receipts(id,candidate_id,fingerprint,data) VALUES($1,$2,$3,$4)`, out.ID, c.ID, fingerprint, data); err != nil {
		return out, false, err
	}
	if err = deliveryAudit(ctx, tx, e, a, "delivery.verification_recorded", out); err != nil {
		return out, false, err
	}
	return out, true, tx.Commit(ctx)
}

func deliveryVerified(d OperatorDelivery, p DeliveryPolicy) bool {
	return deliveryPolicyMatches(d.Candidate, p) && d.Receipt != nil && d.Receipt.Passed && d.Receipt.TestCount >= p.MinTests && d.Receipt.PolicySHA256 == p.PolicySHA256 && d.Receipt.VerifierID == p.VerifierID
}

func (s *Store) ApproveDeliveryCandidate(ctx context.Context, id string, a DeliveryAccess, p DeliveryPolicy, in ApproveDeliveryCandidate) (DeliveryApproval, bool, error) {
	var out DeliveryApproval
	a.Verifier = false
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	e, err := lockDelivery(ctx, tx, id, a)
	if err != nil {
		return out, false, err
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return out, false, err
	}
	if !deliveryVerified(d, p) || d.Candidate.ID != in.CandidateID || d.Receipt.ID != in.ReceiptID || in.PolicySHA256 != p.PolicySHA256 {
		return out, false, ErrExecutionConflict
	}
	actor := a.AuthenticatedBy
	if actor == "" {
		actor = a.Actor
	}
	fingerprint := executionFingerprint([]any{in, actor})
	if d.Approval != nil {
		if executionFingerprint([]any{d.Approval.ApproveDeliveryCandidate, d.Approval.Actor}) != fingerprint {
			return out, false, ErrExecutionConflict
		}
		return *d.Approval, false, tx.Commit(ctx)
	}
	out = DeliveryApproval{ID: executionFingerprint([]string{in.CandidateID, fingerprint})[:32], ApproveDeliveryCandidate: in, Actor: actor, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, `INSERT INTO operator_delivery_approvals(id,candidate_id,fingerprint,data) VALUES($1,$2,$3,$4)`, out.ID, in.CandidateID, fingerprint, data); err != nil {
		return out, false, err
	}
	if err = deliveryAudit(ctx, tx, e, a, "delivery.approved", out); err != nil {
		return out, false, err
	}
	return out, true, tx.Commit(ctx)
}

func (s *Store) ReservePublication(ctx context.Context, id string, a DeliveryAccess, p DeliveryPolicy, in ReservePublication) (PublicationOperation, bool, error) {
	var out PublicationOperation
	a.Verifier = false
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	e, err := lockDelivery(ctx, tx, id, a)
	if err != nil {
		return out, false, err
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return out, false, err
	}
	if d.Operation != nil {
		if executionFingerprint(d.Operation.ReservePublication) != executionFingerprint(in) {
			return out, false, ErrExecutionConflict
		}
		return *d.Operation, false, tx.Commit(ctx)
	}
	if !p.PublicationEnabled || e.Demo || e.State != "completed" || !e.StopConfirmed || !deliveryVerified(d, p) || d.Candidate.Generation != e.Generation || strings.TrimPrefix(in.Branch, "refs/heads/") == d.Candidate.BaseBranch || d.Approval == nil || d.Candidate.ID != in.CandidateID || d.Receipt.ID != in.ReceiptID || d.Approval.ID != in.ApprovalID || d.Approval.PolicySHA256 != p.PolicySHA256 || in.PolicySHA256 != p.PolicySHA256 {
		return out, false, ErrExecutionConflict
	}
	now := time.Now().UTC()
	out = PublicationOperation{ID: in.OperationID, ExecutionID: id, ReservePublication: in, State: "reserved", CanonicalSHA: d.Candidate.CanonicalSHA, RepositoryURL: d.Candidate.RepositoryURL, BaseBranch: d.Candidate.BaseBranch, CreatedAt: now, UpdatedAt: now}
	data, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, `INSERT INTO operator_publication_operations(id,execution_id,candidate_id,fingerprint,data) VALUES($1,$2,$3,$4,$5)`, out.ID, id, in.CandidateID, executionFingerprint(in), data); err != nil {
		return out, false, err
	}
	if err = deliveryAudit(ctx, tx, e, a, "delivery.publication_reserved", out); err != nil {
		return out, false, err
	}
	return out, true, tx.Commit(ctx)
}

func (s *Store) RecordPublicationStatus(ctx context.Context, id, operation string, a DeliveryAccess, in PublicationStatus) (PublicationOperation, error) {
	var out PublicationOperation
	if !a.Verifier {
		return out, ErrExecutionConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	e, err := lockDelivery(ctx, tx, id, a)
	if err != nil {
		return out, err
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if d.Operation == nil || d.Operation.ID != operation {
		return out, ErrExecutionNotFound
	}
	out = *d.Operation
	if in.CanonicalSHA != out.CanonicalSHA || in.Branch != out.Branch || (in.State != "unknown" && in.State != "published") || (in.State == "published" && (in.RemoteID == "" || in.RemoteURL == "")) || (in.State == "unknown" && (in.RemoteID != "" || in.RemoteURL != "")) {
		return out, ErrExecutionConflict
	}
	if out.State == "published" {
		if in.State != "published" || in.RemoteID != out.RemoteID || in.RemoteURL != out.RemoteURL {
			return out, ErrExecutionConflict
		}
		return out, tx.Commit(ctx)
	}
	if out.State == in.State {
		return out, tx.Commit(ctx)
	}
	out.State = in.State
	out.RemoteID = in.RemoteID
	out.RemoteURL = in.RemoteURL
	out.UpdatedAt = time.Now().UTC()
	data, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, `UPDATE operator_publication_operations SET state=$2,data=$3,updated_at=$4 WHERE id=$1`, out.ID, out.State, data, out.UpdatedAt); err != nil {
		return out, err
	}
	if err = deliveryAudit(ctx, tx, e, a, "delivery.publication_"+out.State, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

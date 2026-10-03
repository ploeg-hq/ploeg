package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func deliveryHTTPFixture(t *testing.T) (*Server, string, string, store.OperatorExecution, store.AdmitDeliveryCandidate) {
	t.Helper()
	owners, ownerToken := operatorTestConsumers(t, []string{"silver"}, true)
	verifiers, verifierToken := operatorTestConsumers(t, []string{"silver"}, false)
	verifiers[0].Principal.Name = "verifier"
	verifiers[0].Principal.CanVerify = true
	policy := DeliveryPolicy{RepositoryID: "example", PolicySHA256: strings.Repeat("a", 64), VerifierID: "de-vloer-docker-v1", MinTests: 2, PublicationEnabled: true}
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: append(owners, verifiers...), DeliveryPolicies: map[string]DeliveryPolicy{"example": policy}}}
	input := operatorHTTPInput("delivery-http")
	input.Demo = false
	input.BudgetUSD = 2
	e, _, err := testStore.AdmitOperatorExecution(context.Background(), owners[0].Principal.Name, "alice", input, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e, err = testStore.CommandOperatorExecution(context.Background(), e.ID, owners[0].Principal.Name, "alice", store.OperatorExecutionCommand{ID: "complete", Action: "report", ExpectedRevision: e.Revision, Generation: e.Generation, State: "completed", StopConfirmed: true}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	candidate := store.AdmitDeliveryCandidate{Generation: e.Generation, RepositoryID: input.RepositoryID, RepositoryURL: input.RepositoryURL, BaseSHA: strings.Repeat("b", 40), CanonicalSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40), ArtifactSHA256: strings.Repeat("e", 64), PolicySHA256: policy.PolicySHA256}
	return s, ownerToken, verifierToken, e, candidate
}

func TestOperatorDeliveryHTTPSeparatesVerifierAndExecutionAuthority(t *testing.T) {
	reset(t)
	s, owner, verifier, e, candidate := deliveryHTTPFixture(t)
	path := "/api/v1/operator/executions/" + e.ID + "/delivery"
	if w := operatorExecutionRequest(s, "POST", path+"/candidates", owner, "alice", candidate); w.Code != 403 {
		t.Fatalf("execution consumer self-asserted canonical facts: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", path+"/candidates", verifier, "bob", candidate); w.Code != 404 {
		t.Fatalf("cross-actor candidate: %d %s", w.Code, w.Body)
	}
	s.OperatorConfig.Consumers[1].Principal.Teams = []string{"other"}
	if w := operatorExecutionRequest(s, "POST", path+"/candidates", verifier, "alice", candidate); w.Code != 404 {
		t.Fatalf("cross-team verifier: %d %s", w.Code, w.Body)
	}
	s.OperatorConfig.Consumers[1].Principal.Teams = []string{"silver"}
	if w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions/"+e.ID+"/commands", verifier, "alice", map[string]any{"commandId": "forged", "action": "resume", "expectedRevision": e.Revision, "generation": e.Generation}); w.Code != 403 {
		t.Fatalf("verifier gained execution control: %d %s", w.Code, w.Body)
	}
	w := operatorExecutionRequest(s, "POST", path+"/candidates", verifier, "alice", candidate)
	if w.Code != 201 {
		t.Fatalf("trusted candidate: %d %s", w.Code, w.Body)
	}
	var body struct {
		Candidate store.DeliveryCandidate `json:"candidate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	receipt := store.AdmitVerificationReceipt{CandidateID: body.Candidate.ID, PolicySHA256: candidate.PolicySHA256, CanonicalSHA: candidate.CanonicalSHA, TreeSHA: candidate.TreeSHA, ArtifactSHA256: candidate.ArtifactSHA256, Passed: true, TestCount: 2, VerifierID: "de-vloer-docker-v1", EvidenceSHA256: strings.Repeat("f", 64)}
	if w := operatorExecutionRequest(s, "POST", path+"/verification", owner, "alice", receipt); w.Code != 403 {
		t.Fatalf("worker verification accepted: %d %s", w.Code, w.Body)
	}
	bad := receipt
	bad.TestCount = 0
	if w := operatorExecutionRequest(s, "POST", path+"/verification", verifier, "alice", bad); w.Code != 409 {
		t.Fatalf("no tests blessed: %d %s", w.Code, w.Body)
	}
	w = operatorExecutionRequest(s, "POST", path+"/verification", verifier, "alice", receipt)
	if w.Code != 201 {
		t.Fatalf("verified: %d %s", w.Code, w.Body)
	}
	var verified struct {
		Receipt store.VerificationReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}
	approval := store.ApproveDeliveryCandidate{CandidateID: body.Candidate.ID, ReceiptID: verified.Receipt.ID, PolicySHA256: candidate.PolicySHA256}
	if w := operatorExecutionRequest(s, "POST", path+"/approval", verifier, "alice", approval); w.Code != 403 {
		t.Fatalf("verifier approved for human: %d %s", w.Code, w.Body)
	}
	w = operatorExecutionRequest(s, "POST", path+"/approval", owner, "alice", approval)
	if w.Code != 201 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	var approved struct {
		Approval store.DeliveryApproval `json:"approval"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}
	publication := store.ReservePublication{OperationID: "publication-one", CandidateID: body.Candidate.ID, ReceiptID: verified.Receipt.ID, ApprovalID: approved.Approval.ID, PolicySHA256: candidate.PolicySHA256, Branch: "vloer/publication-one"}
	w = operatorExecutionRequest(s, "POST", path+"/publication", owner, "alice", publication)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"effectAuthorized":true`) {
		t.Fatalf("first reservation: %d %s", w.Code, w.Body)
	}
	w = operatorExecutionRequest(s, "POST", path+"/publication", owner, "alice", publication)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"effectAuthorized":false`) {
		t.Fatalf("lost response replay granted another effect: %d %s", w.Code, w.Body)
	}
	status := store.PublicationStatus{State: "unknown", CanonicalSHA: candidate.CanonicalSHA, Branch: publication.Branch}
	statusPath := path + "/publication/" + publication.OperationID + "/status"
	if w := operatorExecutionRequest(s, "POST", statusPath, verifier, "alice", status); w.Code != 200 {
		t.Fatalf("unknown status: %d %s", w.Code, w.Body)
	}
	status.State = "published"
	status.RemoteID = "42"
	status.RemoteURL = "https://evil.example/pulls/42"
	if w := operatorExecutionRequest(s, "POST", statusPath, verifier, "alice", status); w.Code != 400 {
		t.Fatalf("foreign evidence: %d %s", w.Code, w.Body)
	}
	status.RemoteURL = "https://forge.example/webgrip/example/pulls/42"
	if w := operatorExecutionRequest(s, "POST", statusPath, owner, "alice", status); w.Code != 403 {
		t.Fatalf("untrusted positive evidence: %d %s", w.Code, w.Body)
	}
	if w := operatorExecutionRequest(s, "POST", statusPath, verifier, "alice", status); w.Code != 200 {
		t.Fatalf("positive evidence: %d %s", w.Code, w.Body)
	}
	w = operatorExecutionRequest(s, "GET", path, owner, "alice", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"published"`) || strings.Contains(w.Body.String(), e.RunToken) {
		t.Fatalf("delivery projection: %d %s", w.Code, w.Body)
	}
}

func TestDeliveryPoliciesFailClosedAndRejectUnregisteredInputs(t *testing.T) {
	policies, err := ParseDeliveryPolicies("")
	if err != nil || len(policies) != 0 {
		t.Fatalf("default policy: %v %v", policies, err)
	}
	p := DeliveryPolicy{RepositoryID: "example", PolicySHA256: strings.Repeat("a", 64), VerifierID: "de-vloer-docker-v1", MinTests: 2}
	data, _ := json.Marshal([]DeliveryPolicy{p})
	policies, err = ParseDeliveryPolicies(string(data))
	if err != nil || policies["example"].PublicationEnabled {
		t.Fatalf("policy default enabled effects: %v %v", policies, err)
	}
	for _, raw := range []string{`{}`, `[{"repositoryId":"example","policySha256":"bad","verifierId":"worker","minTests":0}]`, string(data) + ` []`, strings.Replace(string(data), `"minTests":2`, `"minTests":0`, 1), strings.Replace(string(data), `"minTests":2`, `"minTests":2,"workerVerified":true`, 1)} {
		if _, err := ParseDeliveryPolicies(raw); err == nil {
			t.Fatalf("accepted malformed policy %s", raw)
		}
	}
	duplicate, _ := json.Marshal([]DeliveryPolicy{p, p})
	if _, err := ParseDeliveryPolicies(string(duplicate)); err == nil {
		t.Fatal("duplicate policy accepted")
	}
	for _, branch := range []string{"../main", "-option", "refs//heads/a", "a.lock", "a/.hidden", "a/", "a..b"} {
		if validDeliveryBranch(branch) {
			t.Fatalf("unsafe branch %s", branch)
		}
	}
}

func TestOperatorDeliverySchemaTracksWireRecords(t *testing.T) {
	data, err := os.ReadFile("../../docs/contracts/operator-delivery.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"admitCandidate": store.AdmitDeliveryCandidate{}, "candidate": store.DeliveryCandidate{}, "admitReceipt": store.AdmitVerificationReceipt{}, "receipt": store.VerificationReceipt{}, "approveCandidate": store.ApproveDeliveryCandidate{}, "approval": store.DeliveryApproval{}, "reservePublication": store.ReservePublication{}, "operation": store.PublicationOperation{}, "publicationStatus": store.PublicationStatus{}, "delivery": store.OperatorDelivery{}, "policy": DeliveryPolicy{}} {
		fields := map[string]bool{}
		var collect func(reflect.Type)
		collect = func(typ reflect.Type) {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if field.Anonymous {
					collect(field.Type)
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")[0]
				if tag != "" && tag != "-" {
					fields[tag] = true
				}
			}
		}
		collect(reflect.TypeOf(value))
		if len(fields) != len(schema.Defs[name].Properties) {
			t.Fatalf("%s schema has %d properties, wire has %d", name, len(schema.Defs[name].Properties), len(fields))
		}
		for field := range fields {
			if _, ok := schema.Defs[name].Properties[field]; !ok {
				t.Fatalf("%s missing schema property %s", name, field)
			}
		}
	}
}

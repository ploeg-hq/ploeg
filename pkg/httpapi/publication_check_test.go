package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type forgePullRequest struct {
	State      string
	Merged     bool
	HeadSHA    string
	HeadRef    string
	HeadRepo   string
	BaseRef    string
	BaseRepo   string
	AuthorName string
}

type fakePublicationForge struct {
	mu    sync.Mutex
	pr    forgePullRequest
	reads atomic.Int32
	auth  atomic.Value
}

func (f *fakePublicationForge) set(change func(*forgePullRequest)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.pr)
}

func (f *fakePublicationForge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/webgrip/example/pulls/42" {
			http.NotFound(w, r)
			return
		}
		f.reads.Add(1)
		f.auth.Store(r.Header.Get("Authorization"))
		f.mu.Lock()
		pr := f.pr
		f.mu.Unlock()
		fmt.Fprintf(w, `{"state":%q,"merged":%t,"user":{"login":%q},"head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}},"base":{"ref":%q,"repo":{"full_name":%q}}}`,
			pr.State, pr.Merged, pr.AuthorName, pr.HeadRef, pr.HeadSHA, pr.HeadRepo, pr.BaseRef, pr.BaseRepo)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type recordingTracker struct {
	mu       sync.Mutex
	comments []string
}

func (r *recordingTracker) Name() string { return "manual" }
func (r *recordingTracker) ParseWebhook(*http.Request) ([]provider.TrackerEvent, error) {
	return nil, nil
}
func (r *recordingTracker) FetchItem(context.Context, string) (work.WorkItem, error) {
	return work.WorkItem{}, nil
}
func (r *recordingTracker) SetStatus(context.Context, string, work.State) error { return nil }
func (r *recordingTracker) Comment(_ context.Context, _ string, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.comments = append(r.comments, body)
	return nil
}

func (r *recordingTracker) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.comments...)
}

type reservedPublicationFixture struct {
	s          *Server
	owner      string
	verifier   string
	path       string
	statusPath string
	published  store.PublicationStatus
	forge      *fakePublicationForge
	tracker    *recordingTracker
}

func reservePublicationForCheck(t *testing.T) reservedPublicationFixture {
	t.Helper()
	reset(t)
	s, owner, verifier, e, candidate := deliveryHTTPFixture(t)
	path := "/api/v1/operator/executions/" + e.ID + "/delivery"
	decode := func(w *httptest.ResponseRecorder, out any) {
		t.Helper()
		if w.Code != 201 {
			t.Fatalf("delivery step: %d %s", w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var admitted struct {
		Candidate store.DeliveryCandidate `json:"candidate"`
	}
	decode(operatorExecutionRequest(s, "POST", path+"/candidates", verifier, "alice", candidate), &admitted)
	receipt := store.AdmitVerificationReceipt{CandidateID: admitted.Candidate.ID, PolicySHA256: candidate.PolicySHA256, CanonicalSHA: candidate.CanonicalSHA, TreeSHA: candidate.TreeSHA, ArtifactSHA256: candidate.ArtifactSHA256, Passed: true, TestCount: 2, VerifierID: "docker-verifier-v1", EvidenceSHA256: strings.Repeat("f", 64)}
	var verified struct {
		Receipt store.VerificationReceipt `json:"receipt"`
	}
	decode(operatorExecutionRequest(s, "POST", path+"/verification", verifier, "alice", receipt), &verified)
	var approved struct {
		Approval store.DeliveryApproval `json:"approval"`
	}
	decode(operatorExecutionRequest(s, "POST", path+"/approval", owner, "alice", store.ApproveDeliveryCandidate{CandidateID: admitted.Candidate.ID, ReceiptID: verified.Receipt.ID, PolicySHA256: candidate.PolicySHA256}), &approved)
	publication := store.ReservePublication{OperationID: "publication-check", CandidateID: admitted.Candidate.ID, ReceiptID: verified.Receipt.ID, ApprovalID: approved.Approval.ID, PolicySHA256: candidate.PolicySHA256, Branch: "console/publication-check"}
	var reserved struct {
		Operation store.PublicationOperation `json:"operation"`
	}
	decode(operatorExecutionRequest(s, "POST", path+"/publication", owner, "alice", publication), &reserved)

	forge := &fakePublicationForge{pr: forgePullRequest{State: "open", HeadSHA: candidate.CanonicalSHA, HeadRef: publication.Branch, HeadRepo: "webgrip/example",
		BaseRef: "development", BaseRepo: "webgrip/example", AuthorName: "ploeg-publisher"}}
	srv := forge.serve(t)
	s.Forges = map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: srv.URL, Token: "ploegd-read"}}
	tracker := &recordingTracker{}
	s.Trackers = map[string]provider.TrackerProvider{"manual": tracker}
	return reservedPublicationFixture{s: s, owner: owner, verifier: verifier, path: path, statusPath: path + "/publication/" + publication.OperationID + "/status",
		published: store.PublicationStatus{State: "published", CanonicalSHA: candidate.CanonicalSHA, Branch: publication.Branch, RemoteID: "42",
			RemoteURL: "https://forge.example/webgrip/example/pulls/42"},
		forge: forge, tracker: tracker}
}

func (f reservedPublicationFixture) post(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return operatorExecutionRequest(f.s, "POST", f.statusPath, f.verifier, "alice", f.published)
}

func (f reservedPublicationFixture) operationState(t *testing.T) string {
	t.Helper()
	w := operatorExecutionRequest(f.s, "GET", f.path, f.owner, "alice", nil)
	var body struct {
		Delivery store.OperatorDelivery `json:"delivery"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Delivery.Operation == nil {
		t.Fatalf("delivery read: %d %s", w.Code, w.Body)
	}
	return body.Delivery.Operation.State
}

func validDeliveryResponse(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/operator-delivery.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("response violates the delivery schema: %v\n%s", err, w.Body)
	}
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body: %v %s", err, w.Body)
	}
	return body.Error.Code
}

func TestPublicationStatusRejectsMismatchedHead(t *testing.T) {
	f := reservePublicationForCheck(t)
	f.forge.set(func(pr *forgePullRequest) { pr.HeadSHA = strings.Repeat("9", 40) })
	w := f.post(t)
	if w.Code != 409 || errorCode(t, w) != "publication_mismatch" || !strings.Contains(w.Body.String(), "head commit") {
		t.Fatalf("a pull request at another commit was accepted: %d %s", w.Code, w.Body)
	}
	if state := f.operationState(t); state != "reserved" {
		t.Fatalf("a refused report moved the operation to %s", state)
	}
	if got := f.tracker.all(); len(got) != 0 {
		t.Fatalf("a refused report commented: %q", got)
	}
}

func TestPublicationStatusRejectsEveryOtherMismatch(t *testing.T) {
	cases := map[string]struct {
		change func(*forgePullRequest)
		policy func(*DeliveryPolicy)
		want   string
	}{
		"head branch":        {change: func(pr *forgePullRequest) { pr.HeadRef = "console/other" }, want: "head branch"},
		"base branch":        {change: func(pr *forgePullRequest) { pr.BaseRef = "main" }, want: "base branch"},
		"head repository":    {change: func(pr *forgePullRequest) { pr.HeadRepo = "attacker/example" }, want: "repository"},
		"base repository":    {change: func(pr *forgePullRequest) { pr.BaseRepo = "webgrip/other" }, want: "repository"},
		"closed unmerged":    {change: func(pr *forgePullRequest) { pr.State = "closed" }, want: "state"},
		"publisher mismatch": {change: func(pr *forgePullRequest) { pr.AuthorName = "someone-else" }, policy: func(p *DeliveryPolicy) { p.PublisherLogin = "ploeg-publisher" }, want: "author"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := reservePublicationForCheck(t)
			f.forge.set(c.change)
			if c.policy != nil {
				p := f.s.OperatorConfig.DeliveryPolicies["example"]
				c.policy(&p)
				f.s.OperatorConfig.DeliveryPolicies["example"] = p
			}
			w := f.post(t)
			if w.Code != 409 || errorCode(t, w) != "publication_mismatch" || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("mismatched %s accepted: %d %s", name, w.Code, w.Body)
			}
			if state := f.operationState(t); state != "reserved" {
				t.Fatalf("refused report moved the operation to %s", state)
			}
		})
	}
}

func TestPublicationStatusAcceptsAnExactMatchOnceAndReplaysIdempotently(t *testing.T) {
	f := reservePublicationForCheck(t)
	p := f.s.OperatorConfig.DeliveryPolicies["example"]
	p.PublisherLogin = "Ploeg-Publisher"
	f.s.OperatorConfig.DeliveryPolicies["example"] = p
	w := f.post(t)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"published"`) {
		t.Fatalf("exact match: %d %s", w.Code, w.Body)
	}
	validDeliveryResponse(t, w)
	if got := f.forge.auth.Load(); got != "token ploegd-read" {
		t.Fatalf("forge read went out as %v", got)
	}
	reads := f.forge.reads.Load()
	if reads != 1 {
		t.Fatalf("forge reads = %d", reads)
	}
	if w := f.post(t); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"published"`) {
		t.Fatalf("identical replay: %d %s", w.Code, w.Body)
	}
	if got := f.forge.reads.Load(); got != reads {
		t.Fatalf("an identical replay re-read the forge: %d", got)
	}
	comments := f.tracker.all()
	if len(comments) != 1 || !strings.Contains(comments[0], f.published.RemoteURL) {
		t.Fatalf("tracker comments = %q", comments)
	}
	other := f.published
	other.RemoteID = "43"
	other.RemoteURL = "https://forge.example/webgrip/example/pulls/43"
	if w := operatorExecutionRequest(f.s, "POST", f.statusPath, f.verifier, "alice", other); w.Code != 409 {
		t.Fatalf("published barrier accepted other evidence: %d %s", w.Code, w.Body)
	}
}

func TestPublicationStatusAcceptsAMergedPullRequest(t *testing.T) {
	f := reservePublicationForCheck(t)
	f.forge.set(func(pr *forgePullRequest) { pr.State, pr.Merged = "closed", true })
	if w := f.post(t); w.Code != 200 {
		t.Fatalf("merged pull request: %d %s", w.Code, w.Body)
	}
}

func TestPublicationStatusRefusesWithoutAForgeReaderOrAReadablePullRequest(t *testing.T) {
	f := reservePublicationForCheck(t)
	saved := f.s.Forges
	f.s.Forges = nil
	if w := f.post(t); w.Code != 409 || errorCode(t, w) != "publication_unverifiable" {
		t.Fatalf("unverifiable publication: %d %s", w.Code, w.Body)
	}
	f.s.Forges = saved
	notPull := f.published
	notPull.RemoteURL = "https://forge.example/webgrip/example/issues/42"
	if w := operatorExecutionRequest(f.s, "POST", f.statusPath, f.verifier, "alice", notPull); w.Code != 400 || errorCode(t, w) != "invalid_publication_evidence" {
		t.Fatalf("non pull request evidence: %d %s", w.Code, w.Body)
	}
	missing := f.published
	missing.RemoteURL = "https://forge.example/webgrip/example/pulls/7"
	if w := operatorExecutionRequest(f.s, "POST", f.statusPath, f.verifier, "alice", missing); w.Code != 503 || errorCode(t, w) != "forge_unavailable" {
		t.Fatalf("unreadable pull request: %d %s", w.Code, w.Body)
	}
	if state := f.operationState(t); state != "reserved" {
		t.Fatalf("refusals moved the operation to %s", state)
	}
}

func TestDeliveryReadExposesPublicationEnabled(t *testing.T) {
	reset(t)
	s, owner, _, e, _ := deliveryHTTPFixture(t)
	path := "/api/v1/operator/executions/" + e.ID + "/delivery"
	read := func() bool {
		t.Helper()
		w := operatorExecutionRequest(s, "GET", path, owner, "alice", nil)
		var body struct {
			PublicationEnabled *bool `json:"publicationEnabled"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.PublicationEnabled == nil {
			t.Fatalf("delivery read: %d %s", w.Code, w.Body)
		}
		validDeliveryResponse(t, w)
		return *body.PublicationEnabled
	}
	if !read() {
		t.Fatal("an enabled policy read as disabled")
	}
	p := s.OperatorConfig.DeliveryPolicies["example"]
	p.PublicationEnabled = false
	s.OperatorConfig.DeliveryPolicies["example"] = p
	if read() {
		t.Fatal("a disabled policy read as enabled")
	}
	delete(s.OperatorConfig.DeliveryPolicies, "example")
	if read() {
		t.Fatal("a missing policy read as enabled")
	}
}

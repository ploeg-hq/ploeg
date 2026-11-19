package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type prepareFixture struct {
	admissionFixture
	forge  *fakeForge
	issuer *personIssuer
}

func newPrepareFixture(t *testing.T) prepareFixture {
	t.Helper()
	f := newAdmissionFixture(t)
	forge := &fakeForge{states: map[string]provider.RepositoryState{
		"webgrip/app":             {AgentsFile: true},
		"webgrip/homelab-cluster": {},
	}}
	f.server.Readiness = target.NewGate(map[string]work.Target{"app": appTarget, "homelab-cluster": homelabTarget},
		map[string]provider.RepositoryInspector{"forgejo": forge})
	f.server.OperatorConfig.Consumers[0].clientIDs = []string{personClient}
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience,
		Now: func() time.Time { return personNow }}, f.server.OperatorConfig.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.PersonTokens = tokens
	f.server.ForgeTokenIsolation = ForgeTokenIsolationProxy
	return prepareFixture{admissionFixture: f, forge: forge, issuer: is}
}

func prepareBody(requestID string) map[string]any {
	return map[string]any{"requestId": requestID, "team": "bronze", "target": "homelab-cluster", "kind": "prepare_repository"}
}

func (f prepareFixture) proven(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := attributedRequest(f.server, "POST", admissionPath, f.token,
		map[string][]string{PersonTokenHeader: {f.issuer.sign(f.issuer.claims())}}, body)
	validOperatorResponse(t, w)
	return w
}

type prepareError struct {
	Error struct {
		Code             string `json:"code"`
		Message          string `json:"message"`
		PrepareAvailable *bool  `json:"prepareAvailable"`
		WorkItemID       string `json:"workItemId"`
		PullRequestURL   string `json:"pullRequestUrl"`
	} `json:"error"`
}

func decodePrepareError(t *testing.T, w *httptest.ResponseRecorder) prepareError {
	t.Helper()
	var e prepareError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func admittedPrepare(t *testing.T, w *httptest.ResponseRecorder) (int64, admissionResult) {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("prepare admission: %d %s", w.Code, w.Body)
	}
	var got admissionResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(got.WorkItem.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id, got
}

func TestAPrepareAdmissionIsAcceptedForATargetMissingOnlyItsInstructionFile(t *testing.T) {
	f := newPrepareFixture(t)

	id, got := admittedPrepare(t, f.proven(t, prepareBody("prepare-1")))

	item, err := testStore.WorkItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != work.KindPrepareRepository || item.Title != "Add AGENTS.md to webgrip/homelab-cluster" ||
		item.Description != prepareDescription || *item.Target != homelabTarget {
		t.Errorf("stored item = %+v, want Ploeg's prepare title and brief on the target", item)
	}
	if got.WorkItem.BudgetUSD != DefaultPrepareRepositoryMaxBudgetUSD || got.WorkItem.PoolUSD == nil || *got.WorkItem.PoolUSD != DefaultPrepareRepositoryMaxBudgetUSD {
		t.Errorf("budget %v pool %v, want both capped at the default prepare cap", got.WorkItem.BudgetUSD, got.WorkItem.PoolUSD)
	}
	var kind string
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'kind' FROM audit_log WHERE work_item_id=$1 AND action='operator.work_item.admitted'`, id).Scan(&kind); err != nil || kind != "prepare_repository" {
		t.Errorf("admission audit kind = %q (%v)", kind, err)
	}

	replay := f.proven(t, prepareBody("prepare-1"))
	if replay.Code != http.StatusOK {
		t.Errorf("replay: %d %s, want 200", replay.Code, replay.Body)
	}
}

func TestAPrepareAdmissionTakesTheTeamsPrepareCap(t *testing.T) {
	f := newPrepareFixture(t)
	f.server.PrepareRepositoryCaps = map[string]float64{"bronze": 0.75}
	body := prepareBody("prepare-capped")
	body["budgetUsd"] = 10

	_, got := admittedPrepare(t, f.proven(t, body))

	if got.WorkItem.BudgetUSD != 0.75 || got.WorkItem.PoolUSD == nil || *got.WorkItem.PoolUSD != 0.75 {
		t.Errorf("budget %v pool %v, want the Team's prepare cap 0.75", got.WorkItem.BudgetUSD, got.WorkItem.PoolUSD)
	}
}

func TestPrepareAdmissionRefusals(t *testing.T) {
	cases := []struct {
		name    string
		body    func() map[string]any
		forge   map[string]provider.RepositoryState
		status  int
		code    string
		prepare bool
	}{
		{name: "ready target", body: func() map[string]any { b := prepareBody("r"); b["target"] = "app"; return b }, status: 409, code: "target_already_ready"},
		{name: "archived target", forge: map[string]provider.RepositoryState{"webgrip/homelab-cluster": {Archived: true}}, status: 422, code: "target_not_ready"},
		{name: "archived target with its file", forge: map[string]provider.RepositoryState{"webgrip/homelab-cluster": {Archived: true, AgentsFile: true}}, status: 422, code: "target_not_ready"},
		{name: "mirror", forge: map[string]provider.RepositoryState{"webgrip/homelab-cluster": {Mirror: true}}, status: 422, code: "target_not_ready"},
		{name: "with a title", body: func() map[string]any { b := prepareBody("r"); b["title"] = "Also refactor"; return b }, status: 400, code: "invalid_admission"},
		{name: "with a description", body: func() map[string]any { b := prepareBody("r"); b["description"] = "and the CI"; return b }, status: 400, code: "invalid_admission"},
		{name: "unknown kind", body: func() map[string]any { b := prepareBody("r"); b["kind"] = "prepare_ci"; return b }, status: 400, code: "invalid_admission"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrepareFixture(t)
			for repo, state := range tc.forge {
				f.forge.set(repo, state)
			}
			body := prepareBody("r")
			if tc.body != nil {
				body = tc.body()
			}
			w := f.proven(t, body)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			e := decodePrepareError(t, w)
			if e.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", e.Error.Code, tc.code)
			}
			if tc.code == "target_not_ready" && (e.Error.PrepareAvailable == nil || *e.Error.PrepareAvailable) {
				t.Errorf("prepareAvailable = %v, want false", e.Error.PrepareAvailable)
			}
			if n := countRows(t, "SELECT count(*) FROM work_items"); n != 0 {
				t.Errorf("a refused prepare left %d work items", n)
			}
		})
	}
}

func TestASecondOpenPrepareOnTheTargetIsRefusedNamingTheFirst(t *testing.T) {
	f := newPrepareFixture(t)
	first, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-a")))

	w := f.proven(t, prepareBody("prepare-b"))

	if w.Code != http.StatusConflict {
		t.Fatalf("second prepare: %d %s, want 409", w.Code, w.Body)
	}
	e := decodePrepareError(t, w)
	if e.Error.Code != "prepare_in_progress" || e.Error.WorkItemID != strconv.FormatInt(first, 10) ||
		!strings.Contains(e.Error.Message, strconv.FormatInt(first, 10)) {
		t.Errorf("error = %+v, want prepare_in_progress naming %d", e.Error, first)
	}
}

func TestAPrepareAdmissionNeedsAProvenPersonInEveryAttributionMode(t *testing.T) {
	for _, mode := range []AttributionMode{AttributionAsserted, AttributionProven} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPrepareFixture(t)
			f.server.OperatorConfig.Attribution = mode

			w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", prepareBody("prepare-asserted"))

			if w.Code != http.StatusForbidden || operatorErrorCode(t, w) != "person_token_required" {
				t.Fatalf("asserted prepare: %d %s, want 403 person_token_required", w.Code, w.Body)
			}
			if n := countRows(t, "SELECT count(*) FROM work_items"); n != 0 {
				t.Errorf("an asserted prepare left %d work items", n)
			}
			if n := countRows(t, "SELECT count(*) FROM audit_log WHERE action='operator.attribution_refused'"); n != 1 {
				t.Errorf("%d attribution refusals audited, want 1", n)
			}
		})
	}
}

func TestAChangeAdmissionToATargetMissingOnlyItsFileOffersThePrepare(t *testing.T) {
	for name, tc := range map[string]struct {
		state provider.RepositoryState
		want  bool
	}{
		"missing only the file":   {state: provider.RepositoryState{}, want: true},
		"archived as well":        {state: provider.RepositoryState{Archived: true}, want: false},
		"a mirror with its file ": {state: provider.RepositoryState{Mirror: true, AgentsFile: true}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPrepareFixture(t)
			f.forge.set("webgrip/homelab-cluster", tc.state)
			body := admissionBody("change-unready")
			body["target"] = "homelab-cluster"

			w := operatorExecutionRequest(f.server, "POST", admissionPath, f.token, "alice", body)

			if w.Code != 422 {
				t.Fatalf("status = %d, want 422: %s", w.Code, w.Body)
			}
			validOperatorResponse(t, w)
			e := decodePrepareError(t, w)
			if e.Error.Code != "target_not_ready" || e.Error.PrepareAvailable == nil || *e.Error.PrepareAvailable != tc.want {
				t.Errorf("error = %+v, want target_not_ready with prepareAvailable %v", e.Error, tc.want)
			}
		})
	}
}

func TestATrackerItemForATargetMissingOnlyItsFileIsRefusedWithThePrepareOffered(t *testing.T) {
	reset(t)
	board := newBoard(t)
	board.labels["5401"] = []string{"repo/homelab-cluster"}
	forge := readyForge()
	forge.set("webgrip/homelab-cluster", provider.RepositoryState{})
	s := routingServer(t, board, forge, nil)
	consumers, token := operatorTestConsumers(t, []string{AllTeams}, false)
	s.OperatorConfig = OperatorConfig{Consumers: consumers}

	assign(t, s.Handler(), "5401")

	body := readRouteRefusals(t, s, token)
	if len(body.Refusals) != 1 || body.Refusals[0].Code != target.RefusalTargetNotReady || !body.Refusals[0].PrepareAvailable {
		t.Fatalf("refusals = %+v, want one target_not_ready with prepareAvailable", body.Refusals)
	}
	if n := countRows(t, "SELECT count(*) FROM work_items"); n != 0 {
		t.Errorf("tracker intake stored %d work items for an unready target", n)
	}
	if n := countRows(t, "SELECT count(*) FROM work_items WHERE kind='prepare_repository'"); n != 0 {
		t.Errorf("tracker intake created %d prepare work items", n)
	}
}

func TestAPrepareWhoseTargetBecameReadyBeforeTheClaimIsWithdrawnAndStartsNoRun(t *testing.T) {
	f := newPrepareFixture(t)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-ready")))
	f.forge.set("webgrip/homelab-cluster", provider.RepositoryState{AgentsFile: true})

	if status, _ := postClaim(t, f.server.Handler(), proxyClaim); status != http.StatusNoContent {
		t.Fatalf("claim = %d, want no run started", status)
	}

	if got := itemStateOf(t, id); got != string(work.StateWithdrawn) {
		t.Errorf("item state = %q, want withdrawn", got)
	}
	if n := countRows(t, "SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action=$2", id, store.ActionPrepareNotNeeded); n != 1 {
		t.Errorf("%d prepare_not_needed events, want 1", n)
	}
	if n := countRows(t, "SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action='work_item.withdrawn' AND detail->>'reason'=$2", id, store.CloseReasonAlreadyPrepared); n != 1 {
		t.Errorf("%d withdrawals with already_prepared, want 1", n)
	}
	if n := countRows(t, "SELECT count(*) FROM agent_runs WHERE work_item_id=$1 AND state IN ('running','pending')", id); n != 0 {
		t.Errorf("%d runs still live, want none", n)
	}
}

func TestAPrepareWhoseTargetWasArchivedBeforeTheClaimEndsStuck(t *testing.T) {
	f := newPrepareFixture(t)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-archived")))
	f.forge.set("webgrip/homelab-cluster", provider.RepositoryState{Archived: true})

	if status, _ := postClaim(t, f.server.Handler(), proxyClaim); status != http.StatusNoContent {
		t.Fatalf("claim = %d, want no run started", status)
	}

	var outcome, reason string
	if err := testPool.QueryRow(context.Background(), `SELECT outcome, stuck_reason FROM agent_runs WHERE work_item_id=$1 AND state='finished' ORDER BY id DESC LIMIT 1`, id).Scan(&outcome, &reason); err != nil {
		t.Fatal(err)
	}
	if outcome != string(work.OutcomeStuck) || !strings.Contains(reason, "is archived") {
		t.Errorf("run outcome %q reason %q, want stuck naming the archive", outcome, reason)
	}
}

func TestAPrepareWhoseTargetStillMissesOnlyItsFileStartsItsWriter(t *testing.T) {
	f := newPrepareFixture(t)
	admittedPrepare(t, f.proven(t, prepareBody("prepare-claim")))

	status, resp := postClaim(t, f.server.Handler(), proxyClaim)

	if status != http.StatusOK || resp.WorkItem.Kind != work.KindPrepareRepository {
		t.Fatalf("claim = %d kind %q, want the prepare writer started", status, resp.WorkItem.Kind)
	}
}

func itemStateOf(t *testing.T, id int64) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM work_items WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

type forgejoPullRequests struct {
	mu        sync.Mutex
	state     string
	refuse    bool
	closed    []string
	comments  []string
	deletions int
	// lateBranch, when set, is the head of an open pull request the forge
	// lists, as a Run still running at a cancel would open it.
	lateBranch string
}

func (fp *forgejoPullRequests) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/7"):
		_ = json.NewEncoder(w).Encode(map[string]any{"state": fp.state, "merged": false, "head": map[string]any{"sha": "abc"}})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
		if fp.lateBranch == "" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"number": 7, "head": map[string]any{"ref": fp.lateBranch,
			"repo": map[string]any{"full_name": "webgrip/homelab-cluster"}}}})
	case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/pulls/7"):
		if fp.refuse {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fp.state = "closed"
		fp.closed = append(fp.closed, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "closed"})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fp.comments = append(fp.comments, body.Body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "body": body.Body})
	case r.Method == http.MethodDelete:
		fp.deletions++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (f prepareFixture) withForgejo(t *testing.T, fp *forgejoPullRequests) {
	t.Helper()
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)
	f.engine.Forges = map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: srv.URL, Secret: "shh", Token: "t"}}
	f.engine.DefaultForge = "forgejo"
}

func (f prepareFixture) openPullRequest(t *testing.T, id int64) {
	t.Helper()
	status, claim := postClaim(t, f.server.Handler(), proxyClaim)
	if status != http.StatusOK {
		t.Fatalf("claim = %d", status)
	}
	if _, err := testStore.ReportOutcome(context.Background(), claim.RunToken, store.Report(work.OutcomePROpened, "opened",
		"", []string{"https://forgejo.example/webgrip/homelab-cluster/pulls/7"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.EvaluateItem(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func (f prepareFixture) cancel(t *testing.T, id int64) map[string]any {
	t.Helper()
	w := operatorExecutionRequest(f.server, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/cancel", id), f.token, "alice", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	validOperatorResponse(t, w)
	var body struct {
		Cancellation map[string]any `json:"cancellation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Cancellation
}

func TestCancellingAPrepareClosesItsPullRequestAndAllowsANewOne(t *testing.T) {
	f := newPrepareFixture(t)
	fp := &forgejoPullRequests{state: "open"}
	f.withForgejo(t, fp)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-cancel")))
	f.openPullRequest(t, id)

	got := f.cancel(t, id)

	if got["withdrawn"] != true {
		t.Fatalf("cancellation = %v, want withdrawn", got)
	}
	pr, _ := got["pullRequest"].(map[string]any)
	if pr["state"] != "closed" || !strings.HasSuffix(fmt.Sprint(pr["url"]), "/pulls/7") {
		t.Errorf("cancellation.pullRequest = %v, want pull request 7 closed", pr)
	}
	if len(fp.closed) != 1 || fp.deletions != 0 {
		t.Errorf("forge closed %v and deleted %d, want one close and the branch kept", fp.closed, fp.deletions)
	}
	if len(fp.comments) != 1 || !strings.Contains(fp.comments[0], "alice cancelled the prepare") {
		t.Errorf("comments = %q, want one naming who cancelled", fp.comments)
	}
	if n := countRows(t, "SELECT count(*) FROM audit_log WHERE work_item_id=$1 AND action=$2 AND detail->>'closed'='true'", id, store.ActionPrepareCancelled); n != 1 {
		t.Errorf("%d prepare_cancelled events with the pull request closed, want 1", n)
	}

	admittedPrepare(t, f.proven(t, prepareBody("prepare-after-cancel")))
}

func TestAPrepareCancelTheForgeCouldNotCloseBlocksANewPrepareUntilAPersonClosesIt(t *testing.T) {
	f := newPrepareFixture(t)
	fp := &forgejoPullRequests{state: "open", refuse: true}
	f.withForgejo(t, fp)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-stuck-pr")))
	f.openPullRequest(t, id)

	got := f.cancel(t, id)

	pr, _ := got["pullRequest"].(map[string]any)
	if got["withdrawn"] != true || pr["state"] != "open" || !strings.HasSuffix(fmt.Sprint(pr["url"]), "/pulls/7") {
		t.Fatalf("cancellation = %v, want withdrawn with pull request 7 named as still open", got)
	}
	w := f.proven(t, prepareBody("prepare-blocked"))
	if w.Code != http.StatusConflict {
		t.Fatalf("new prepare: %d %s, want 409", w.Code, w.Body)
	}
	e := decodePrepareError(t, w)
	if e.Error.Code != "prepare_in_progress" || !strings.HasSuffix(e.Error.PullRequestURL, "/pulls/7") || e.Error.WorkItemID != strconv.FormatInt(id, 10) {
		t.Errorf("error = %+v, want prepare_in_progress naming the open pull request", e.Error)
	}

	if got := f.itemPullRequestState(t, id); got != "open" {
		t.Errorf("work item pullRequest.state = %q, want open", got)
	}

	fp.mu.Lock()
	fp.state = "closed"
	fp.mu.Unlock()
	if got := f.itemPullRequestState(t, id); got != "closed" {
		t.Errorf("work item pullRequest.state = %q, want closed after a person closed it", got)
	}
	admittedPrepare(t, f.proven(t, prepareBody("prepare-after-person-closed")))
}

func (f prepareFixture) itemPullRequestState(t *testing.T, id int64) string {
	t.Helper()
	var body struct {
		Item struct {
			Kind        string `json:"kind"`
			PullRequest *struct {
				URL   string `json:"url"`
				State string `json:"state"`
			} `json:"pullRequest"`
		} `json:"item"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, f.server, f.token, fmt.Sprintf("work-items/%d", id)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Item.Kind != "prepare_repository" || body.Item.PullRequest == nil {
		t.Fatalf("item = %+v, want a prepare with its pull request", body.Item)
	}
	return body.Item.PullRequest.State
}

func TestTheTeamsReadShowsEachTeamsPrepareCap(t *testing.T) {
	f := newPrepareFixture(t)
	f.server.PrepareRepositoryCaps = map[string]float64{"silver": 5}
	f.server.OperatorConfig.Teams = map[string][]string{"bronze": {"builder", "reviewer"}, "silver": {"builder"}}
	var body struct {
		Teams []struct {
			ID                string `json:"id"`
			PrepareRepository *struct {
				MaxBudgetUSD float64 `json:"maxBudgetUsd"`
			} `json:"prepareRepository"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, f.server, f.token, "teams"), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, team := range body.Teams {
		if team.PrepareRepository != nil {
			got[team.ID] = team.PrepareRepository.MaxBudgetUSD
		}
	}
	if got["bronze"] != DefaultPrepareRepositoryMaxBudgetUSD || got["silver"] != 5 {
		t.Errorf("prepare caps = %v, want bronze at the default and silver at 5", got)
	}
}

func TestConnectionsOfferThePrepareForARegisteredTargetMissingOnlyItsFile(t *testing.T) {
	forge := &fakeForge{states: map[string]provider.RepositoryState{"webgrip/app": {AgentsFile: true}, "webgrip/homelab-cluster": {}}}
	c := &Connections{
		Repositories: []ConnectionRepository{{Key: "app", Label: "app", Target: appTarget}, {Key: "homelab-cluster", Label: "homelab-cluster", Target: homelabTarget},
			{Key: "webgrip/homelab-cluster@main", Target: homelabTarget}},
		Readiness: target.NewGate(map[string]work.Target{"app": appTarget, "homelab-cluster": homelabTarget, "webgrip/homelab-cluster@main": homelabTarget},
			map[string]provider.RepositoryInspector{"forgejo": forge}),
	}
	got := map[string]bool{}
	for _, repo := range c.Repositories {
		got[repo.Key] = connectionRepository(context.Background(), c, repo, nil, nil).PrepareAvailable
	}
	if got["app"] || !got["homelab-cluster"] || got["webgrip/homelab-cluster@main"] {
		t.Errorf("prepareAvailable = %v, want only the registered target missing its file", got)
	}
}

const proxyClaim = `{"team":"bronze","role":"builder","harness":"openhands","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`

func TestAPrepareIsRefusedForATeamWhoseWriterWouldBypassTheDeliveryFence(t *testing.T) {
	cases := map[string]struct {
		setup  func(f prepareFixture)
		body   func() map[string]any
		reason string
	}{
		"workers without the forge proxy": {setup: func(f prepareFixture) { f.server.ForgeTokenIsolation = "" }, reason: "PLOEG_WORKER_FORGE_TOKEN_ISOLATION is not proxy"},
		"a client-carried writer": {setup: func(f prepareFixture) {
			f.server.OperatorConfig.TeamHarnesses = TeamHarnesses{"bronze": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: claudeEnvironmentHarness}, Carrier: store.CarrierClient}}}
		}, reason: "carried by a person's own client"},
		"a writer in a Claude environment": {setup: func(f prepareFixture) {
			f.server.OperatorConfig.TeamHarnesses = TeamHarnesses{"bronze": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: claudeEnvironmentHarness}}}}
		}, reason: "runs in a Claude environment"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPrepareFixture(t)
			tc.setup(f)

			w := f.proven(t, prepareBody("prepare-unfenced"))

			e := decodePrepareError(t, w)
			if w.Code != 422 || e.Error.Code != "prepare_unsupported_for_team" || !strings.Contains(e.Error.Message, tc.reason) {
				t.Fatalf("admission = %d %+v, want 422 prepare_unsupported_for_team naming %q", w.Code, e.Error, tc.reason)
			}
			if n := countRows(t, "SELECT count(*) FROM work_items"); n != 0 {
				t.Errorf("a refused prepare left %d work items", n)
			}
		})
	}
}

func TestAPrepareWriterIsNeverClaimedByAWorkerWithoutTheForgeProxy(t *testing.T) {
	for name, body := range map[string]string{
		"no isolation":         `{"team":"bronze","role":"builder","harness":"openhands","llmKeyIsolation":"proxy"}`,
		"a Claude environment": `{"team":"bronze","role":"builder","harness":"claude-environment","forgeTokenIsolation":"proxy","llmKeyIsolation":"proxy"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPrepareFixture(t)
			id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-unfenced-claim")))

			rec := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/claim", strings.NewReader(body)))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), credentialIsolationRequired) {
				t.Fatalf("claim = %d %s, want 403 %s", rec.Code, rec.Body, credentialIsolationRequired)
			}
			if got := runStates(t, id); got != "pending" {
				t.Errorf("runs = %q, want the prepare Run still pending for a worker behind the proxy", got)
			}
		})
	}
}

func TestAPersonsClientCannotClaimAPrepare(t *testing.T) {
	f := newPrepareFixture(t)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-client")))
	f.server.OperatorConfig.TeamHarnesses = TeamHarnesses{"bronze": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: claudeEnvironmentHarness}, Carrier: store.CarrierClient}}}

	w := operatorExecutionRequest(f.server, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/claim", id), f.token, "alice", nil)

	if w.Code != 422 || operatorErrorCode(t, w) != "prepare_unsupported_for_team" {
		t.Fatalf("client claim = %d %s, want 422 prepare_unsupported_for_team", w.Code, w.Body)
	}
}

func TestAPrepareIsNeverTakenOver(t *testing.T) {
	f := newPrepareFixture(t)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-take-over")))
	if status, _ := postClaim(t, f.server.Handler(), proxyClaim); status != http.StatusOK {
		t.Fatalf("claim = %d", status)
	}

	w := operatorExecutionRequest(f.server, "POST", fmt.Sprintf("/api/v1/operator/work-items/%d/take-over", id), f.token, "alice",
		map[string]any{"commandId": "take-prepare"})

	if w.Code != http.StatusConflict || operatorErrorCode(t, w) != "prepare_not_taken_over" {
		t.Fatalf("take-over = %d %s, want 409 prepare_not_taken_over", w.Code, w.Body)
	}
	if n := countRows(t, "SELECT count(*) FROM work_item_take_overs WHERE work_item_id=$1", id); n != 0 {
		t.Errorf("%d take-overs recorded, want none", n)
	}
}

func TestAPullRequestARunOpenedAfterItsPrepareWasCancelledBlocksTheNextPrepare(t *testing.T) {
	f := newPrepareFixture(t)
	fp := &forgejoPullRequests{state: "open"}
	f.withForgejo(t, fp)
	id, _ := admittedPrepare(t, f.proven(t, prepareBody("prepare-late-pr")))
	got := f.cancel(t, id)
	if got["withdrawn"] != true || got["pullRequest"] != nil {
		t.Fatalf("cancellation = %v, want withdrawn with no pull request yet", got)
	}
	item, err := testStore.WorkItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	fp.lateBranch = work.Branch(item)
	fp.mu.Unlock()

	w := f.proven(t, prepareBody("prepare-after-late-pr"))

	e := decodePrepareError(t, w)
	if w.Code != http.StatusConflict || e.Error.Code != "prepare_in_progress" || e.Error.WorkItemID != strconv.FormatInt(id, 10) ||
		!strings.Contains(e.Error.Message, "#7") {
		t.Fatalf("admission = %d %+v, want prepare_in_progress naming pull request 7 of the cancelled item", w.Code, e.Error)
	}

	fp.mu.Lock()
	fp.lateBranch = ""
	fp.mu.Unlock()
	admittedPrepare(t, f.proven(t, prepareBody("prepare-after-late-pr-closed")))
}

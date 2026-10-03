package shiftengine

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// --- fakes ------------------------------------------------------------------

// fakeComment is one stored conversation comment. ID is assigned by Comment so
// EditComment can address it, exactly as a real forge does.
type fakeComment struct {
	ID   int64
	Repo string
	PR   int
	Body string
}

type fakeForge struct {
	mu       sync.Mutex
	comments []fakeComment
	nextID   int64
	err      error
	// listErr fails Comments only; editErr fails EditComment only. Separate so
	// a test can make the list fail without also making every write fail.
	listErr  error
	editErr  error
	prStates map[int]provider.PullRequestState
	prFacts  map[int]provider.PullRequestFacts
	reads    []int
	// listCalls counts Comments invocations, and edits records the comment ids
	// edited in order — enough to prove find-then-edit rather than re-post.
	listCalls int
	edits     []int64
}

func (f *fakeForge) PullRequestState(_ context.Context, _ string, pr int) (provider.PullRequestState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, pr)
	if f.err != nil {
		return "", f.err
	}
	if s, ok := f.prStates[pr]; ok {
		return s, nil
	}
	return provider.PullRequestOpen, nil
}

func (f *fakeForge) PullRequestFacts(ctx context.Context, repo string, pr int) (provider.PullRequestFacts, error) {
	state, err := f.PullRequestState(ctx, repo, pr)
	if err != nil {
		return provider.PullRequestFacts{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	facts := f.prFacts[pr]
	facts.State = state
	return facts, nil
}

func (f *fakeForge) Name() string                                              { return "webgrip" }
func (f *fakeForge) ParseWebhook(*http.Request) ([]provider.ForgeEvent, error) { return nil, nil }
func (f *fakeForge) Comment(_ context.Context, repo string, pr int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.nextID++
	f.comments = append(f.comments, fakeComment{ID: f.nextID, Repo: repo, PR: pr, Body: body})
	return nil
}

func (f *fakeForge) Comments(_ context.Context, repo string, pr int) ([]provider.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []provider.Comment
	for _, c := range f.comments {
		if c.Repo == repo && c.PR == pr {
			out = append(out, provider.Comment{ID: c.ID, Body: c.Body})
		}
	}
	return out, nil
}

func (f *fakeForge) EditComment(_ context.Context, repo string, pr int, id int64, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	for i := range f.comments {
		if f.comments[i].ID == id {
			f.comments[i].Body = body
			f.edits = append(f.edits, id)
			return nil
		}
	}
	return errors.New("fakeForge: no such comment")
}

// commentsMatching returns the bodies of this fake's comments whose body
// starts with marker.
func (f *fakeForge) commentsMatching(marker string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), marker) {
			out = append(out, c.Body)
		}
	}
	return out
}

type fakeTracker struct {
	mu       sync.Mutex
	comments []string
	statuses []work.State
	err      error
}

func (f *fakeTracker) Name() string                                                { return "vikunja" }
func (f *fakeTracker) ParseWebhook(*http.Request) ([]provider.TrackerEvent, error) { return nil, nil }
func (f *fakeTracker) FetchItem(context.Context, string) (work.WorkItem, error) {
	return work.WorkItem{}, errors.New("not used")
}
func (f *fakeTracker) Comment(_ context.Context, _, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.comments = append(f.comments, body)
	return nil
}
func (f *fakeTracker) SetStatus(_ context.Context, _ string, s work.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, s)
	return nil
}

// --- unit: PR link parsing --------------------------------------------------

func TestPRNumber(t *testing.T) {
	for in, want := range map[string]int{
		"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7":  7,
		"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7/": 7,
		"https://github.com/o/r/pull/123":                    123,
		"https://forgejo/webgrip/ploeg/issues/7":             0,
		"https://forgejo/webgrip/ploeg":                      0,
		"":                                                   0,
		"not a url":                                          0,
	} {
		if got := prNumber(in); got != want {
			t.Errorf("prNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

// The writer's links carry the PR; readers open none. The most recent one
// wins so a re-opened PR supersedes an earlier link.
func TestPullRequestFromReports(t *testing.T) {
	link, n := pullRequest([]store.RunReport{
		{Role: "analyst", Round: 1},
		{Role: "builder", Round: 2, Writes: true, Links: []string{"https://forgejo/webgrip/ploeg/pulls/7"}},
	})
	if n != 7 || !strings.HasSuffix(link, "/7") {
		t.Errorf("pullRequest = (%q, %d)", link, n)
	}
	if _, n := pullRequest([]store.RunReport{{Role: "analyst", Round: 1}}); n != 0 {
		t.Errorf("a shift with no PR reported %d", n)
	}
}

func TestFindingsCommentIsAttributed(t *testing.T) {
	c := findingsComment(store.RunReport{
		Role: "security", Round: 1, Summary: "reviewed the diff",
		Findings: "- the token is logged at debug",
	})
	for _, want := range []string{"security", "round 1", "reviewed the diff", "token is logged"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment missing %q:\n%s", want, c)
		}
	}
}

func TestFindingsCommentFromAWriterDoesNotClaimItCouldNotPush(t *testing.T) {
	c := findingsComment(store.RunReport{Role: "builder", Round: 2, Writes: true, Findings: "### Ploeg verification"})
	if strings.Contains(c, "could not push") || !strings.Contains(c, "writing Run that pushed") {
		t.Errorf("writer comment footer is wrong:\n%s", c)
	}
	r := findingsComment(store.RunReport{Role: "reviewer", Round: 3, Findings: "ok"})
	if !strings.Contains(r, "could not push") {
		t.Errorf("reader comment lost its footer:\n%s", r)
	}
}

// --- integration: publication through the lifecycle -------------------------

func TestPublish_FindingsReachThePullRequestWhenTheRoundCompletes(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	tracker := &fakeTracker{}
	e := newEngine(bronzePlan(10))
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}

	// An item with a resolved target, so ploegd knows the repository.
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "900", Team: "bronze", Title: "t",
		ExternalScope: "11", RouteRule: "11/bronze",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}

	// Round 1 readers report findings. No PR exists yet, so nothing is
	// published — the findings still reach the writer via the briefing.
	for _, role := range []string{"analyst", "tests"} {
		r, err := testStore.ClaimRole(ctx, "bronze", role, time.Minute, 1)
		if err != nil {
			t.Fatalf("claim %s: %v", role, err)
		}
		if _, err := testStore.ReportOutcome(ctx, r.RunToken,
			store.Report(work.OutcomeNoChangeNeeded, "read it", "", nil, nil, nil).
				WithFindings("## "+role+"\n- something to fix")); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 0 {
		t.Errorf("published before a PR existed: %+v", forge.comments)
	}

	// Round 2: the writer opens the PR.
	rw, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatalf("claim builder: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/7"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	// The plan is exhausted, so the shift closed and the human was told.
	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want 1", len(tracker.comments))
	}
	if !strings.Contains(tracker.comments[0], "pulls/7") {
		t.Errorf("tracker comment lacks the PR link:\n%s", tracker.comments[0])
	}
	if !strings.Contains(tracker.comments[0], "review and merge") {
		t.Errorf("tracker comment does not ask for a merge:\n%s", tracker.comments[0])
	}
	if len(tracker.statuses) != 1 || tracker.statuses[0] != work.StateAwaitingReview {
		t.Errorf("statuses = %v, want [awaiting_review]", tracker.statuses)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q", got)
	}
}

// Findings survive the pod: once a PR exists, a later round's reader reaches
// the thread (blackboard spec, "a human sees the same thread").
func TestPublish_ReaderAfterThePRExists(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := newEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "901", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}

	// Round 1: writer opens the PR.
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/9"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	// Round 2: the reviewer's findings must land on that PR.
	rr, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim reviewer: %v", err)
	}
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- the retry loop is unbounded")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if len(forge.comments) != 1 {
		t.Fatalf("forge comments = %d, want 1: %+v", len(forge.comments), forge.comments)
	}
	c := forge.comments[0]
	if c.Repo != "webgrip/ploeg" || c.PR != 9 {
		t.Errorf("published to %s#%d, want webgrip/ploeg#9", c.Repo, c.PR)
	}
	if !strings.Contains(c.Body, "reviewer") || !strings.Contains(c.Body, "unbounded") {
		t.Errorf("comment body = %q", c.Body)
	}
}

// A forge outage must not lose the Outcome or stall the Shift: the state
// transition still happens and the item still reaches a person.
func TestPublish_ForgeFailureDoesNotBlockTheLifecycle(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{err: errors.New("forge is down")}
	tracker := &fakeTracker{err: errors.New("tracker is down")}
	e := newEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "902", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo/webgrip/ploeg/pulls/9"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("a forge outage propagated into the lifecycle: %v", err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- something")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("a tracker outage propagated into the lifecycle: %v", err)
	}

	if si, _ := testStore.LiveShiftForItem(ctx, id); si != nil {
		t.Error("shift stayed open because publication failed")
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review despite the outages", got)
	}
}

// An unresolved Work Target means ploegd genuinely does not know the
// repository. Publishing to a guess would be worse than not publishing.
func TestPublish_SkippedWhenTargetUnresolved(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := newEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "903", Team: "bronze", Title: "t", // no Target
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo/webgrip/ploeg/pulls/9"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- something")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 0 {
		t.Errorf("published against an unresolved target: %+v", forge.comments)
	}
}

// The engine looks up a forge by the ID the Work Target carries, which is an
// INSTANCE identifier and not the provider's dialect name (ADR-0016). Keying
// the registry by Name() would silently match nothing and skip every
// publication — found by wiring the two ends together, not by either alone.
func TestPublish_ForgeIsKeyedByTargetIDNotDialectName(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{} // Name() is "webgrip" here on purpose: id != dialect
	e := newEngine(reviewPlan())
	// Registered under the TARGET'S id.
	e.Forges = map[string]provider.ForgeProvider{"webgrip-forgejo": forge}

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "910", Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip-forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo/webgrip/ploeg/pulls/3"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- something")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("findings not published: the forge registry is keyed wrongly (%d comments)", len(forge.comments))
	}
}

// The 2026-07-30 incident, as a test. Every routing rule in production omits
// `forge:`, so every Target carried the empty string, and the empty string was
// used as a literal registry key — which matched nothing, so a real review
// with verdict=approve was written to the database and never reached the pull
// request. `pkg/work.Target` documented "empty = the default forge" the whole
// time; only the documentation implemented it.
//
// The sibling test above looks like it covers this and does not: it asserts
// the engine uses the TARGET's id, which was never broken, and it hand-writes
// a non-empty id on both sides. Fails against v0.2.0-rc.14.
func TestPublish_EmptyForgeIDUsesTheDefault(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := newEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"forgejo": forge}
	e.DefaultForge = "forgejo"

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "911", Team: "bronze", Title: "t",
		// No Forge — exactly what NewMapResolver produces for a rule that
		// names no forge, which is all seven of them in production.
		Target: &work.Target{Owner: "webgrip", Repo: "erfbeeld", BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo/webgrip/erfbeeld/pulls/9"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- Verdict: Approve.")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("a review with no forge id on its target reached nobody (%d comments); "+
			"empty must resolve to DefaultForge", len(forge.comments))
	}
}

// And the other direction: with no DefaultForge configured there is nothing to
// fall back to, and publishing to a guess would be worse than not publishing.
// A single registered provider must NOT be inferred — with two forges the
// guess posts an internal review onto an unrelated public pull request.
func TestPublish_EmptyForgeIDWithNoDefaultPublishesNothing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e := newEngine(reviewPlan())
	e.Forges = map[string]provider.ForgeProvider{"forgejo": forge}
	e.DefaultForge = ""

	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: "912", Team: "bronze", Title: "t",
		Target: &work.Target{Owner: "webgrip", Repo: "erfbeeld", BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken,
		store.Report(work.OutcomePROpened, "opened", "",
			[]string{"https://forgejo/webgrip/erfbeeld/pulls/9"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken,
		store.Report(work.OutcomeNoChangeNeeded, "reviewed", "", nil, nil, nil).
			WithFindings("- Verdict: Approve.")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 0 {
		t.Fatalf("published to an inferred forge (%d comments); the fallback must be a configured id, not a guess",
			len(forge.comments))
	}
}

// The 2026-07-30 incident, as a test. A plan-less team opened a real pull
// request, the item settled `done` — and the board was told nothing, because
// the write-back was gated on needs_human. Fails against v0.2.0-rc.12.
func TestPublish_DoneOutcomeStillNotifiesTheTracker(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	tracker := &fakeTracker{}
	e := uniformEngine(t)
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	id, _ := openUniform(t, e, "580")

	run, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(
		work.OutcomePROpened, "opened a PR", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/30"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}

	if got := itemState(t, id); got != "awaiting_review" {
		t.Fatalf("item state = %q, want awaiting_review (the path that used to skip the write-back)", got)
	}
	if len(tracker.comments) != 1 {
		t.Fatalf("tracker comments = %d, want exactly 1 — a finished PR must reach the board", len(tracker.comments))
	}
	if !strings.Contains(tracker.comments[0], "/pulls/30") {
		t.Errorf("comment carries no pull request link:\n%s", tracker.comments[0])
	}
	// Definition of Done here is "in production, monitored, first telemetry
	// observed" — none of which Ploeg can see. It must never close the task.
	for _, s := range tracker.statuses {
		if s == work.StateDone {
			t.Error("Ploeg closed the tracker task; a PR is open and unmerged, so the item is not done")
		}
	}
}

// A failed run under the retry threshold re-queues. That is not terminal, and
// announcing "Ploeg stopped working this item" mid-retry would be a lie.
func TestPublish_FailedRetryDoesNotNotify(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	tracker := &fakeTracker{}
	e := uniformEngine(t)
	e.Trackers = map[string]provider.TrackerProvider{"vikunja": tracker}
	id, _ := openUniform(t, e, "581")

	run, _ := testStore.ClaimRole(ctx, "silver", "", time.Minute, 0)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken,
		store.Report(work.OutcomeFailed, "boom", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := itemState(t, id); got != "queued" {
		t.Fatalf("item state = %q, want queued for a retry", got)
	}
	if len(tracker.comments) != 0 {
		t.Errorf("tracker was told about a retry: %v", tracker.comments)
	}
}

// --- unit: usage report publication -----------------------------------------

// usageReportEnv opens a planned Shift with a resolved target and an enabled
// usage report, returning the item id and the fake forge.
func usageReportEnv(t *testing.T, e *Engine, externalID string) (int64, *fakeForge) {
	t.Helper()
	ctx := context.Background()
	resetTables(t)
	forge := &fakeForge{}
	e.Forges = map[string]provider.ForgeProvider{"webgrip": forge}
	e.UsageReport = true
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "webgrip", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := testStore.WorkItem(ctx, id)
	if err := e.EnsureShift(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	return id, forge
}

func TestPublishUsage_CreatesOnceThenEditsInPlace(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "980")
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}

	// Round 1: the writer opens the pull request. The report is created.
	rw, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/40"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	created := forge.commentsMatching(usageReportMarker)
	if len(created) != 1 {
		t.Fatalf("report comments after the first round = %d, want 1", len(created))
	}

	// Round 2: the reviewer finishes. The Shift then closes (plan exhausted).
	// Every refresh edits the same comment, never posts another.
	rr, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken, store.Report(work.OutcomeNoChangeNeeded, "reviewed", "",
		nil, nil, nil).WithFindings("- one thing")); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	after := forge.commentsMatching(usageReportMarker)
	if len(after) != 1 {
		t.Fatalf("report comments after the second round = %d, want exactly 1 (edited in place)", len(after))
	}
	if len(forge.edits) == 0 {
		t.Errorf("the report was never edited in place")
	}
	if !strings.Contains(after[0], "reviewer") {
		t.Errorf("edited report did not pick up the second Run:\n%s", after[0])
	}

	// A refresh from the settlement sweep edits the same comment again; the
	// Shift is closed by now, which is exactly the late-settlement case.
	editsBefore := len(forge.edits)
	if err := e.RefreshUsageReport(ctx, si.ID); err != nil {
		t.Fatalf("RefreshUsageReport: %v", err)
	}
	if got := len(forge.commentsMatching(usageReportMarker)); got != 1 {
		t.Fatalf("a refresh duplicated the report: %d comments", got)
	}
	if len(forge.edits) != editsBefore+1 {
		t.Errorf("edits = %v, want one more after the refresh", forge.edits)
	}
}

// A marker on a later page is edited, not duplicated (task 1.4).
func TestPublishUsage_FindsMarkerOnALaterPage(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "981")
	// Seed the forge with a report comment that the engine did not create —
	// as if a previous ploegd had opened it.
	if err := forge.Comment(ctx, "webgrip/ploeg", 41, usageReportMarker+"\n\n### Ploeg usage report\n\nstale\n"); err != nil {
		t.Fatal(err)
	}
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/41"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	got := forge.commentsMatching(usageReportMarker)
	if len(got) != 1 {
		t.Fatalf("report comments = %d, want 1 (edited, not duplicated)", len(got))
	}
	if strings.Contains(got[0], "stale") {
		t.Errorf("the stale marker comment was not the one edited:\n%s", got[0])
	}
}

func TestPublishUsage_SkipsWithoutAPullRequest(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	// A resolved target but no writing Run, so there is no pull request to post to.
	id, forge := usageReportEnv(t, e, "982")
	_ = id
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	e.publishUsageReport(ctx, *si)
	if len(forge.comments) != 0 {
		t.Errorf("published a report with no pull request: %+v", forge.comments)
	}
}

func TestPublishUsage_SkipsWhenDisabled(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "983")
	e.UsageReport = false
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/42"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.commentsMatching(usageReportMarker)) != 0 {
		t.Error("the report was published while PLOEG_USAGE_REPORT was off")
	}
}

func TestPublishUsage_ListFailureDoesNotPost(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "984")
	forge.listErr = errors.New("forge list is down")
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/43"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("a list failure propagated into the lifecycle: %v", err)
	}
	if len(forge.comments) != 0 {
		t.Errorf("posted blind despite a failed list (duplicate risk): %+v", forge.comments)
	}
}

func TestPublishUsage_EditFailureChangesNothing(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "985")
	if err := forge.Comment(ctx, "webgrip/ploeg", 44, usageReportMarker+"\n\nold\n"); err != nil {
		t.Fatal(err)
	}
	forge.editErr = errors.New("edit rejected")
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/44"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("an edit failure propagated into the lifecycle: %v", err)
	}
	// Finish the plan too, so the Shift closes despite the edit failures.
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken, store.Report(work.OutcomeNoChangeNeeded, "reviewed", "",
		nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatalf("an edit failure propagated into the lifecycle: %v", err)
	}
	if got := len(forge.commentsMatching(usageReportMarker)); got != 1 {
		t.Errorf("an edit failure posted a duplicate: %d report comments", got)
	}
	if s := itemState(t, id); s != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review despite the edit failure", s)
	}
}

// The report publishes even when a Round produced no findings at all.
func TestEnginePublishesReportAfterARoundWithoutFindings(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "986")
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	// pr_opened, no findings.
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/45"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(forge.commentsMatching(usageReportMarker)) != 1 {
		t.Fatalf("no report published for a round without findings: %+v", forge.comments)
	}
	if !strings.Contains(forge.commentsMatching(usageReportMarker)[0], "Verification: not recorded") {
		t.Errorf("evidence did not degrade to not recorded:\n%s", forge.commentsMatching(usageReportMarker)[0])
	}
}

// On close the report is refreshed from the terminal branch, even when the
// caller passes nil reports (the floor-close path).
func TestEnginePublishesReportOnClose(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "987")
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/46"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Round 2 completes, exhausting the plan and closing the Shift.
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken, store.Report(work.OutcomeNoChangeNeeded, "reviewed", "",
		nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, closed, reason := shiftRow(t, id)
	if !closed || reason != reasonPlanExhausted {
		t.Fatalf("shift not closed by the plan: closed=%v reason=%q", closed, reason)
	}
	// The close branch refreshed the one comment rather than posting a second.
	if got := len(forge.commentsMatching(usageReportMarker)); got != 1 {
		t.Fatalf("report comments after close = %d, want 1", got)
	}
	if len(forge.edits) == 0 {
		t.Errorf("close did not edit the existing report")
	}
}

// The report changes no lifecycle state: the Shift closes and the item settles
// exactly as it would without it.
func TestPublishUsage_DoesNotChangeOutcome(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, _ := usageReportEnv(t, e, "988")
	rw, _ := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 3)
	if _, err := testStore.ReportOutcome(ctx, rw.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forgejo.webgrip.dev/webgrip/ploeg/pulls/47"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	rr, _ := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Minute, 1)
	if _, err := testStore.ReportOutcome(ctx, rr.RunToken, store.Report(work.OutcomeNoChangeNeeded, "reviewed", "",
		nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.EvaluateItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	round, closed, reason := shiftRow(t, id)
	if !closed || reason != reasonPlanExhausted || round != 2 {
		t.Errorf("shift row = round %d, closed %v, reason %q", round, closed, reason)
	}
	if got := itemState(t, id); got != "awaiting_review" {
		t.Errorf("item state = %q, want awaiting_review", got)
	}
}

// RefreshUsageReport on a Shift with no pull request is a silent no-op, not an
// error (task 5.3).
func TestEngineRefreshUsageReportWithoutPullRequestIsSilent(t *testing.T) {
	ctx := context.Background()
	e := newEngine(reviewPlan())
	id, forge := usageReportEnv(t, e, "989")
	si, err := testStore.LiveShiftForItem(ctx, id)
	if err != nil || si == nil {
		t.Fatalf("no live shift: %v", err)
	}
	if err := e.RefreshUsageReport(ctx, si.ID); err != nil {
		t.Fatalf("RefreshUsageReport returned an error for a PR-less shift: %v", err)
	}
	if len(forge.comments) != 0 {
		t.Errorf("a PR-less refresh posted something: %+v", forge.comments)
	}
}

// The wording per terminal state, without a database.
func TestTrackerMessage_PerTerminalState(t *testing.T) {
	const link = "https://forgejo.webgrip.dev/webgrip/ploeg/pulls/30"
	for _, tc := range []struct {
		name    string
		settled work.State
		link    string
		want    string
		absent  string
	}{
		{"ready for review", work.StateAwaitingReview, link, "pull request is ready for review", "stopped working this item"},
		{"done with a PR", work.StateDone, link, "opened a pull request", "No pull request"},
		{"done with nothing to change", work.StateDone, "", "without needing to change anything", "Please review"},
		{"gave up", work.StateStale, "", "gave up on this item", "Please review"},
		// The regression: a CONFIGURED plan settles needs_human on SUCCESS —
		// the last word is "a person is asked to merge". Keying the opening
		// line on the state alone meant every successful multi-Round Shift
		// announced "Ploeg stopped working this item" directly above the pull
		// request it had just produced. A PR existing outranks the state.
		{"reviewed and waiting on a human", work.StateNeedsHuman, link, "opened a pull request", "stopped working this item"},
		{"parked with nothing to show", work.StateNeedsHuman, "", "without opening a pull request", "opened a pull request.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := trackerMessage(tc.settled, "plan_exhausted", tc.link, 1, 1, nil, false)
			if strings.Contains(got, "Budget exhausted") {
				t.Errorf("message claims budget exhaustion without a ledger:\n%s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("message missing %q:\n%s", tc.want, got)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("message should not contain %q:\n%s", tc.absent, got)
			}
			if !strings.Contains(got, "stays open until it is in production") {
				t.Errorf("message drops the definition-of-done note:\n%s", got)
			}
		})
	}
}

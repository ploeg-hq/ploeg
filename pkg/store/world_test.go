package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type world struct {
	t   *testing.T
	ctx context.Context
	now time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	resetTables(t)
	return &world{t: t, ctx: context.Background(), now: time.Now().UTC().Truncate(time.Second)}
}

func (w *world) item(externalID, team string) int64 {
	w.t.Helper()
	id, _, err := testStore.IngestAssigned(w.ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team,
		Title: "Item " + externalID, Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) move(externalID string, g GateMove) bool {
	w.t.Helper()
	g.Provider, g.ExternalID = "vikunja", externalID
	recorded, err := testStore.RecordGateMove(w.ctx, g)
	if err != nil {
		w.t.Fatal(err)
	}
	return recorded
}

type factsPlay struct {
	Number       int `json:"number"`
	Commits      *int
	ForcePushes  *int `json:"forcePushes"`
	ChangedPaths *struct {
		HeadSHA   string        `json:"headSha"`
		Truncated bool          `json:"truncated"`
		Paths     []ChangedPath `json:"paths"`
	} `json:"changedPaths"`
	Events []struct {
		Kind  string `json:"kind"`
		Actor string `json:"actor"`
	} `json:"events"`
	CIRuns []struct {
		Key    string  `json:"key"`
		Status string  `json:"status"`
		Jobs   []CIJob `json:"jobs"`
	} `json:"ciRuns"`
	Files []struct {
		Path        string `json:"path"`
		Indentation *struct {
			Added int `json:"added"`
		} `json:"indentation"`
	} `json:"files"`
}

func (w *world) plays(id int64) []factsPlay {
	w.t.Helper()
	facts, err := testStore.WorkItemFacts(w.ctx, id, nil, FactsOptions{Now: w.now})
	if err != nil {
		w.t.Fatal(err)
	}
	raw, err := json.Marshal(facts.PullRequests)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []factsPlay
	if err := json.Unmarshal(raw, &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) play(id int64) factsPlay {
	w.t.Helper()
	plays := w.plays(id)
	if len(plays) != 1 {
		w.t.Fatalf("plays = %+v; want one", plays)
	}
	return plays[0]
}

func intp(n int) *int { return &n }

package httpapi

import (
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func TestClaim_TellsTheRunTheMostRoundsItsShiftCanRun(t *testing.T) {
	reset(t)
	plans := plan.Plans{"bronze": {MaxFixRounds: 2, Rounds: []plan.Round{
		{Roles: []plan.Role{{Name: "builder", Writes: true}}},
		{Roles: []plan.Role{{Name: "reviewer"}}},
	}}}
	h := apiServer(t, plans)
	shiftFixture(t, "5702", 0, []store.Role{{Name: "builder", Writes: true}})
	if code, claim := postClaim(t, h, `{"team":"bronze","role":"builder"}`); code != http.StatusOK || claim.MaxRounds != 6 {
		t.Fatalf("claim: %d maxRounds=%d, want 6: two planned Rounds and two for each of two fix Rounds", code, claim.MaxRounds)
	}

	shiftFixture(t, "5703", 0, []store.Role{{Name: "builder", Writes: true}})
	if code, claim := postClaim(t, apiServer(t, plan.Plans{}), `{"team":"bronze","role":"builder"}`); code != http.StatusOK || claim.MaxRounds != 0 {
		t.Fatalf("a team without a plan: %d maxRounds=%d, want it absent", code, claim.MaxRounds)
	}
}

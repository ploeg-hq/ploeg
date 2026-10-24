package httpapi

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/consumers"
	"github.com/ploeg-hq/ploeg/pkg/contracts"
)

func TestEveryRecordedConsumerExpectationHolds(t *testing.T) {
	sets, err := consumers.Load("../../docs/contracts/consumers")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) == 0 {
		t.Fatal("docs/contracts/consumers holds no expectation set, so nothing checks that a change keeps its consumers working")
	}
	provider := consumers.Provider{ContractsDir: "../../docs/contracts", Served: contracts.Lookup, Routes: (&Server{}).ServesRoute}
	for _, set := range sets {
		t.Run(set.Name(), func(t *testing.T) {
			for _, problem := range provider.Verify(set) {
				t.Error(problem)
			}
		})
	}
}

func TestServesRouteMatchesMethodAndPath(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/v1/operator/version", true},
		{"GET", "/api/v1/operator/work-items/1", true},
		{"POST", "/api/v1/operator/work-items/1/cancel", true},
		{"POST", "/api/v1/claim", true},
		{"DELETE", "/api/v1/operator/work-items/1", false},
		{"GET", "/api/v1/operator/no-such-route", false},
		{"GET", "/api/v2/operator/teams", false},
	} {
		if got := s.ServesRoute(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

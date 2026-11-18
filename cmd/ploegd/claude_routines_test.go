package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func routineHarnesses() httpapi.TeamHarnesses {
	return httpapi.TeamHarnesses{"brass": {"builder": {
		OperatorRoleHarness: store.OperatorRoleHarness{Name: "claude-environment"},
		Carrier:             store.CarrierRoutine, EnvironmentID: "ccpool_brass",
	}}}
}

func TestRoutinesRefuseToStartWithoutTheCredentialGateway(t *testing.T) {
	t.Setenv("PLOEG_CLAUDE_ROUTINES", "true")
	t.Setenv("PLOEG_CREDENTIAL_GATEWAYS", "")
	if _, err := claudeRoutines(slog.New(slog.DiscardHandler), routineHarnesses()); err == nil || !strings.Contains(err.Error(), "PLOEG_CREDENTIAL_GATEWAYS") {
		t.Fatalf("routines without the gateway: err = %v, want a refusal naming PLOEG_CREDENTIAL_GATEWAYS", err)
	}
	t.Setenv("PLOEG_CREDENTIAL_GATEWAYS", `[{"namespace":"runs"}]`)
	routines, err := claudeRoutines(slog.New(slog.DiscardHandler), routineHarnesses())
	if err != nil || routines == nil || !routines.GatewayMode {
		t.Fatalf("routines with the gateway = %+v, %v", routines, err)
	}
}

func TestARoutineCarrierIsRefusedWhileRoutinesAreOff(t *testing.T) {
	t.Setenv("PLOEG_CLAUDE_ROUTINES", "")
	if _, err := claudeRoutines(slog.New(slog.DiscardHandler), routineHarnesses()); err == nil || !strings.Contains(err.Error(), "carrier routine") {
		t.Fatalf("carrier routine with routines off: err = %v, want a refusal", err)
	}
	if routines, err := claudeRoutines(slog.New(slog.DiscardHandler), httpapi.TeamHarnesses{}); err != nil || routines != nil {
		t.Fatalf("no routines and no routine Role = %+v, %v, want nothing enabled and no error", routines, err)
	}
}

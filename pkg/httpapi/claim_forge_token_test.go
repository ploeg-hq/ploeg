package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type mintingBroker struct{ forgebroker.Static }

func (mintingBroker) Mint(context.Context, forgebroker.MintRequest) (forgebroker.Credential, error) {
	return forgebroker.Credential{ID: "42", Token: "minted-for-this-run"}, nil
}

func targetedWriterShift(t *testing.T, externalID string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Owner: "webgrip", Repo: "fixture"},
	})
	if err != nil {
		t.Fatalf("IngestAssigned: %v", err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 10)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
}

func forgeClaimServer(creds forgebroker.Broker) http.Handler {
	return (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		ForgeCreds:     creds,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
	}).Handler()
}

func TestClaim_NeverReturnsTheSharedForgeToken(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "990")
	h := forgeClaimServer(forgebroker.Static{Token: "the-shared-org-wide-pat"})

	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if resp.ForgeToken != "" || resp.ForgeTokenPerRun {
		t.Errorf("claim carried forge token %q (per-run %v); the shared token must stay in the worker's environment", resp.ForgeToken, resp.ForgeTokenPerRun)
	}
}

func TestClaim_ReturnsAMintedForgeToken(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "991")
	h := forgeClaimServer(mintingBroker{})

	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	if resp.ForgeToken != "minted-for-this-run" || !resp.ForgeTokenPerRun {
		t.Errorf("claim carried forge token %q (per-run %v), want the minted per-run token", resp.ForgeToken, resp.ForgeTokenPerRun)
	}
}

type failingMintBroker struct{ forgebroker.Static }

func (failingMintBroker) Mint(context.Context, forgebroker.MintRequest) (forgebroker.Credential, error) {
	return forgebroker.Credential{}, errors.New("forgejo token API: HTTP 401")
}

func TestClaim_AFailedMintEndsTheRunAsAnInfraNodeFailure(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "992")
	h := forgeClaimServer(failingMintBroker{})

	code, _ := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if code != http.StatusNoContent {
		t.Fatalf("claim returned %d, want 204: nothing may run without its push credential", code)
	}
	var outcome, reason string
	if err := testPool.QueryRow(context.Background(),
		`SELECT outcome, failure_reason FROM agent_runs WHERE state = 'finished'`).Scan(&outcome, &reason); err != nil {
		t.Fatalf("read the released Run: %v", err)
	}
	if outcome != string(work.OutcomeFailed) || reason != string(work.FailureInfraNode) {
		t.Errorf("released Run = %s/%s, want failed/infra_node", outcome, reason)
	}
}

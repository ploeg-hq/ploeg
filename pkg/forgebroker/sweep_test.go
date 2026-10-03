package forgebroker

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func leasedRuns(runTokens ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return runTokens, nil }
}

// A claim takes its Lease before it mints, so a token the listing shows
// belongs to a Run that the Lease read after it reports. Reading the Leases
// first let a token minted mid-sweep look orphaned, and the sweep revoked a
// running worker's push rights.
func TestSweepOrphans_SparesATokenMintedWhileTheSweepRuns(t *testing.T) {
	listed := false
	var deleted []string
	srv := forgejoTokensAPI(t, "agent-builder", "bot-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		listed = true
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "name": namePrefix + "ccccccccdddd-webgrip-ploeg"}})
	})
	leased := func(context.Context) ([]string, error) {
		if !listed {
			return nil, nil
		}
		return []string{"ccccccccdddd0123456789"}, nil
	}

	b := &Forgejo{BaseURL: srv.URL, Bot: "agent-builder", Password: "bot-password"}
	if n, err := b.SweepOrphans(context.Background(), leased); err != nil || n != 0 {
		t.Fatalf("SweepOrphans = %d, %v; want 0, nil", n, err)
	}
	if len(deleted) != 0 {
		t.Errorf("the sweep revoked a leased Run's token: %v", deleted)
	}
}

// Forgejo pages the token list; an orphan past the first page must still be
// found.
func TestSweepOrphans_PagesThroughEveryToken(t *testing.T) {
	var deleted []string
	srv := forgejoTokensAPI(t, "agent-builder", "bot-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			parts := strings.Split(r.URL.Path, "/")
			deleted = append(deleted, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
			return
		}
		page := []map[string]any{}
		switch r.URL.Query().Get("page") {
		case "1":
			for i := range sweepPageSize {
				page = append(page, map[string]any{"id": i, "name": "human-" + strconv.Itoa(i)})
			}
		case "2":
			page = append(page, map[string]any{"id": 99, "name": namePrefix + "eee-webgrip-ploeg"})
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	b := &Forgejo{BaseURL: srv.URL, Bot: "agent-builder", Password: "bot-password"}
	n, err := b.SweepOrphans(context.Background(), leasedRuns())
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphans = %d, %v; want 1, nil", n, err)
	}
	if len(deleted) != 1 || deleted[0] != namePrefix+"eee-webgrip-ploeg" {
		t.Errorf("deleted = %v, want the orphan on page 2", deleted)
	}
}

// One token the forge refuses to delete must not shield the others, and the
// error surfaces so the failure is logged and the next sweep retries it.
func TestSweepOrphans_ARefusedRevokeDoesNotStopTheRest(t *testing.T) {
	var deleted []string
	srv := forgejoTokensAPI(t, "agent-builder", "bot-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if strings.Contains(r.URL.Path, "aaa") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": namePrefix + "aaa-webgrip-ploeg"},
			{"id": 2, "name": namePrefix + "bbb-webgrip-ploeg"},
		})
	})

	b := &Forgejo{BaseURL: srv.URL, Bot: "agent-builder", Password: "bot-password"}
	n, err := b.SweepOrphans(context.Background(), leasedRuns())
	if err == nil || n != 1 || len(deleted) != 1 {
		t.Errorf("SweepOrphans = %d, %v, deleted %v; want 1 revoked and an error", n, err, deleted)
	}
}

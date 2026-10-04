package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// APIClient authenticates bootstrap claims and run-scoped control operations.
type APIClient struct {
	Base           string
	HC             *http.Client
	BootstrapToken string
	WorkerID       string
	controlTokens  sync.Map
}

type ClaimResponse struct {
	RunToken     string        `json:"runToken"`
	ControlToken string        `json:"controlToken,omitempty"`
	Deadline     time.Time     `json:"deadline"`
	WorkItem     work.WorkItem `json:"workItem"`
	// Shift fields, all zero on a pre-Shift claim.
	Shift  int64  `json:"shift,omitempty"`
	Role   string `json:"role,omitempty"`
	Round  int    `json:"round,omitempty"`
	Writes bool   `json:"writes,omitempty"`
	// Branch is derived by ploegd at Shift open; empty = derive it locally.
	Branch string `json:"branch,omitempty"`
	// Authorized is the USD ceiling this run's LLM credential must be minted
	// at (ADR-0012). Zero = fall back to the worker's env budget.
	Authorized float64           `json:"authorized,omitempty"`
	Briefing   []harness.Finding `json:"briefing,omitempty"`
	// ForgeToken is a push credential minted for this run alone and revoked
	// when it settles (ADR-0013 tier 2). Empty = use the env credential.
	ForgeToken string `json:"forgeToken,omitempty"`
	// ForgeTokenPerRun distinguishes a minted credential from the shared
	// token, which arrives in the same field. Only ploegd knows the
	// difference, so it has to say.
	ForgeTokenPerRun bool `json:"forgeTokenPerRun,omitempty"`
	// Planner selects the planner prompt (ADR-0031): the Run splits or
	// clarifies its Work Item and returns created Work Items, not code.
	Planner bool `json:"planner,omitempty"`
	// PreAuthor is true for a reading Run that no writing Run precedes in its
	// Shift. Only such a Run reviews the base branch when the Shift's branch
	// is absent; any other reader fails instead.
	PreAuthor bool `json:"preAuthor,omitempty"`
	// Context names the files people attached to the Work Item before this
	// claim. The worker downloads and verifies each before the harness runs.
	Context []harness.ContextRef `json:"context,omitempty"`
}

// Claim returns nil when the queue is empty (HTTP 204) — the empty-handed
// worker convention (backlog #49). An exhausted Shift budget answers 204 too:
// there is nothing for this pod to do, and parking the item is ploegd's job.
//
// An empty role is the pre-Shift claim, byte-identical to before.
func (a *APIClient) Claim(team, role string) (*ClaimResponse, error) {
	req := map[string]string{"team": team}
	if role != "" {
		req["role"] = role
	}
	body, _ := json.Marshal(req)
	resp, err := a.request(context.Background(), http.MethodPost, "/api/v1/claim", body, a.BootstrapToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var c ClaimResponse
		if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
			return nil, err
		}
		if a.BootstrapToken != "" && c.ControlToken == "" {
			return nil, fmt.Errorf("managed claim returned no run capability")
		}
		if c.ControlToken != "" {
			a.controlTokens.Store(c.RunToken, c.ControlToken)
		}
		return &c, nil
	default:
		return nil, fmt.Errorf("claim: HTTP %d", resp.StatusCode)
	}
}

// Renew returns gone=true when the lease is not ours anymore (404).
func (a *APIClient) Renew(token string) (bool, error) {
	resp, err := a.request(context.Background(), http.MethodPost, "/api/v1/runs/"+token+"/renew", nil, a.runCredential(token))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return false, nil
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		return true, nil
	default:
		return false, fmt.Errorf("renew: HTTP %d", resp.StatusCode)
	}
}

func (a *APIClient) Checkpoint(token string, cp work.Checkpoint) error {
	return a.post("/api/v1/runs/"+token+"/checkpoint", cp)
}

// Outcome posts the full OutcomeReport (schema
// docs/contracts/outcomereport.v1.schema.json); a final checkpoint rides
// inline instead of a separate call.
func (a *APIClient) Outcome(token string, rep harness.OutcomeReport) error {
	return a.post("/api/v1/runs/"+token+"/outcome", rep)
}

// ContextBundle downloads one context item with the Run's capability. It
// reads at most one byte more than the claim declared, so a longer body is
// caught by the caller's size check rather than read without bound.
func (a *APIClient) ContextBundle(ctx context.Context, runToken string, ref harness.ContextRef) ([]byte, error) {
	resp, err := a.request(ctx, http.MethodGet, "/api/v1/runs/"+runToken+"/context/"+url.PathEscape(ref.ID), nil, a.runCredential(runToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("context download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, ref.Bytes+1))
}

func (a *APIClient) post(path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/runs/"), "/")
	resp, err := a.request(context.Background(), http.MethodPost, path, body, a.runCredential(parts[0]))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("run control: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *APIClient) runCredential(runToken string) string {
	if token, ok := a.controlTokens.Load(runToken); ok {
		return token.(string)
	}
	return ""
}

func (a *APIClient) request(ctx context.Context, method, path string, body []byte, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid run control endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if a.WorkerID != "" {
		req.Header.Set("X-Ploeg-Worker-ID", a.WorkerID)
	}
	resp, err := a.HC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("run control request failed")
	}
	return resp, nil
}

func (a *APIClient) Mint(ctx context.Context, req llmbroker.MintRequest) (llmbroker.Credential, error) {
	var cred llmbroker.Credential
	resp, err := a.request(ctx, http.MethodPost, "/api/v1/runs/"+req.RunToken+"/llm/credential", nil, a.runCredential(req.RunToken))
	if err != nil {
		return cred, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cred, fmt.Errorf("managed credential issue: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&cred); err != nil {
		return cred, fmt.Errorf("invalid managed credential response")
	}
	if cred.APIKey == "" || cred.Alias == "" {
		return cred, fmt.Errorf("managed credential response is incomplete")
	}
	cred.RunToken = req.RunToken
	return cred, nil
}

func (a *APIClient) Revoke(ctx context.Context, cred llmbroker.Credential) error {
	resp, err := a.request(ctx, http.MethodPost, "/api/v1/runs/"+cred.RunToken+"/llm/block", nil, a.runCredential(cred.RunToken))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("managed credential block: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *APIClient) Spend(ctx context.Context, cred llmbroker.Credential) (float64, error) {
	resp, err := a.request(ctx, http.MethodGet, "/api/v1/runs/"+cred.RunToken+"/llm/spend", nil, a.runCredential(cred.RunToken))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("managed usage read: HTTP %d", resp.StatusCode)
	}
	var usage struct {
		CostUSD *float64 `json:"costUsd"`
	}
	if json.NewDecoder(resp.Body).Decode(&usage) != nil || usage.CostUSD == nil {
		return 0, fmt.Errorf("managed usage unavailable")
	}
	return *usage.CostUSD, nil
}

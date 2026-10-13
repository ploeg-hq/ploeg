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
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// APIClient authenticates bootstrap claims and run-scoped control operations.
type APIClient struct {
	Base           string
	HC             *http.Client
	BootstrapToken string
	WorkerID       string
	// Harness and HarnessProfile are sent with each claim, so ploegd records
	// which harness and agent profile ran the Run (ADR-0091).
	Harness        string
	HarnessProfile string
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
	// ForgeTokenExpiresAt is when the forge ends ForgeToken by itself; nil
	// for a token that lives until it is revoked.
	ForgeTokenExpiresAt *time.Time `json:"forgeTokenExpiresAt,omitempty"`
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
// An empty role is the pre-Shift claim.
func (a *APIClient) Claim(team, role string) (*ClaimResponse, error) {
	req := map[string]string{"team": team}
	if role != "" {
		req["role"] = role
	}
	if a.Harness != "" {
		req["harness"] = a.Harness
	}
	if a.HarnessProfile != "" {
		req["harnessProfile"] = a.HarnessProfile
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

// Transcript ships one batch of the Run's transcript (ADR-0090). An answer
// that says ploegd will not take it wraps transcript.ErrRefused.
func (a *APIClient) Transcript(ctx context.Context, runToken string, entries []transcript.Entry) error {
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return err
	}
	resp, err := a.request(ctx, http.MethodPost, "/api/v1/runs/"+runToken+"/transcript", body, a.runCredential(runToken))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("transcript: HTTP %d", resp.StatusCode)
	default:
		return fmt.Errorf("transcript: HTTP %d: %w", resp.StatusCode, transcript.ErrRefused)
	}
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

// remoteSession is the claude-environment adapter's view of ploegd's record
// of the session it dispatched for one Run (ADR-0084).
type remoteSession struct {
	api   *APIClient
	token string
}

func (r remoteSession) Record(_ context.Context, s harness.RemoteSession) error {
	return r.api.post("/api/v1/runs/"+r.token+"/claude-session", map[string]string{
		"environmentId": s.EnvironmentID, "sessionId": s.SessionID,
		"baseBranch": s.BaseBranch, "title": s.Title, "reference": s.Reference,
	})
}

func (r remoteSession) State(ctx context.Context) (harness.RemoteSessionState, error) {
	resp, err := r.api.request(ctx, http.MethodGet, "/api/v1/runs/"+r.token+"/claude-session", nil, r.api.runCredential(r.token))
	if err != nil {
		return harness.RemoteSessionState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return harness.RemoteSessionState{}, fmt.Errorf("run control: HTTP %d", resp.StatusCode)
	}
	var body struct {
		ReportedAt   *time.Time `json:"reportedAt"`
		ExitReason   string     `json:"exitReason"`
		PushedCommit string     `json:"pushedCommit"`
		PullRequest  string     `json:"pullRequest"`
		FinalMessage string     `json:"finalMessage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return harness.RemoteSessionState{}, err
	}
	return harness.RemoteSessionState{
		Reported: body.ReportedAt != nil, ExitReason: body.ExitReason,
		PushedCommit: body.PushedCommit, PullRequest: body.PullRequest, FinalMessage: body.FinalMessage,
	}, nil
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

// RefreshForgeCredential asks ploegd for a new push credential for the Run's
// repository; ploegd revokes the one it replaces.
func (a *APIClient) RefreshForgeCredential(ctx context.Context, runToken string) (string, time.Time, error) {
	resp, err := a.request(ctx, http.MethodPost, "/api/v1/runs/"+runToken+"/forge-credential", nil, a.runCredential(runToken))
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("forge credential refresh: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ForgeToken          string     `json:"forgeToken"`
		ForgeTokenExpiresAt *time.Time `json:"forgeTokenExpiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ForgeToken == "" {
		return "", time.Time{}, fmt.Errorf("invalid forge credential response")
	}
	var expires time.Time
	if out.ForgeTokenExpiresAt != nil {
		expires = *out.ForgeTokenExpiresAt
	}
	return out.ForgeToken, expires, nil
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

// Knowledge reads what the Tenant's graphs hold for the Run's own scopes
// (ADR-0087, proposed). ploegd picks the scopes; the request names only the
// Run.
func (a *APIClient) Knowledge(ctx context.Context, runToken string) (harness.KnowledgeResponse, error) {
	var out harness.KnowledgeResponse
	resp, err := a.request(ctx, http.MethodGet, "/api/v1/runs/"+runToken+"/knowledge", nil, a.runCredential(runToken))
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, &knowledgeHTTPError{status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return out, fmt.Errorf("knowledge: %w", err)
	}
	return out, nil
}

type knowledgeHTTPError struct{ status int }

func (e *knowledgeHTTPError) Error() string { return fmt.Sprintf("knowledge: HTTP %d", e.status) }

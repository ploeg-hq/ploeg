package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type LLMPolicy struct {
	Team      string   `json:"team"`
	Role      string   `json:"role"`
	BudgetUSD float64  `json:"budgetUsd"`
	Models    []string `json:"models"`
	TTL       string   `json:"ttl"`
	// LiteLLMTeamID mints the Run's key inside this LiteLLM team (its
	// team_id). MCPAccessGroups grants the key those MCP access groups, which
	// the team must allow. Both empty = no team and no MCP tools (ADR-0078).
	LiteLLMTeamID   string   `json:"litellmTeamId,omitempty"`
	MCPAccessGroups []string `json:"mcpAccessGroups,omitempty"`
}

type ManagedLLMBroker interface {
	llmbroker.Broker
	RevokeForRun(context.Context, string) error
	SpendForRun(context.Context, string) (float64, error)
}

type LLMControl struct {
	Store    *store.Store
	Broker   ManagedLLMBroker
	Policies []LLMPolicy
	// CorrectionWindow is how long a settlement read from the gateway's spend
	// logs stays provisional and is read again for late charges. Zero makes
	// the first settlement final.
	CorrectionWindow time.Duration
}

func NewLLMControl(st *store.Store, broker ManagedLLMBroker, policiesJSON string) (*LLMControl, error) {
	c := &LLMControl{Store: st, Broker: broker}
	if broker == nil {
		return nil, fmt.Errorf("managed LLM broker is required")
	}
	if err := json.Unmarshal([]byte(policiesJSON), &c.Policies); err != nil {
		return nil, fmt.Errorf("invalid managed LLM policy configuration")
	}
	seen := map[string]bool{}
	for _, p := range c.Policies {
		ttl, err := time.ParseDuration(p.TTL)
		key := p.Team + "/" + p.Role
		if p.Team == "" || p.BudgetUSD <= 0 || math.IsNaN(p.BudgetUSD) || math.IsInf(p.BudgetUSD, 0) || len(p.Models) == 0 || err != nil || ttl < time.Second || ttl > 24*time.Hour || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate managed LLM policy")
		}
		for _, m := range p.Models {
			if strings.TrimSpace(m) == "" {
				return nil, fmt.Errorf("empty model scope")
			}
		}
		if err := validMCPGrant(p); err != nil {
			return nil, err
		}
		seen[key] = true
	}
	return c, nil
}

// validMCPGrant refuses MCP access groups without the LiteLLM team that
// bounds them, and blank or repeated group names (ADR-0078).
func validMCPGrant(p LLMPolicy) error {
	if p.LiteLLMTeamID != strings.TrimSpace(p.LiteLLMTeamID) {
		return fmt.Errorf("invalid LiteLLM team id")
	}
	if len(p.MCPAccessGroups) > 0 && p.LiteLLMTeamID == "" {
		return fmt.Errorf("MCP access groups need a LiteLLM team id")
	}
	groups := map[string]bool{}
	for _, g := range p.MCPAccessGroups {
		if strings.TrimSpace(g) == "" || g != strings.TrimSpace(g) || groups[g] {
			return fmt.Errorf("invalid or duplicate MCP access group")
		}
		groups[g] = true
	}
	return nil
}

func (c *LLMControl) Reserve(ctx context.Context, runToken string) error {
	run, err := c.Store.RunControl(ctx, runToken)
	if err != nil {
		return err
	}
	for _, p := range c.Policies {
		if p.Team == run.Team && p.Role == run.Role {
			ttl, err := time.ParseDuration(p.TTL)
			if err != nil {
				return err
			}
			return c.Store.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: runToken, Alias: litellm.Alias(runToken), Authorized: p.BudgetUSD, Models: p.Models, TTLSeconds: int64(ttl.Seconds()),
				GatewayTeamID: p.LiteLLMTeamID, MCPAccessGroups: p.MCPAccessGroups})
		}
	}
	return fmt.Errorf("run has no managed inference policy")
}

func (c *LLMControl) Issue(ctx context.Context, runToken string) (llmbroker.Credential, error) {
	return c.issue(ctx, runToken, c.Store.BeginLLMMint, c.Store.RecordLLMIssued)
}

func (c *LLMControl) IssueOperator(ctx context.Context, runToken, executionID string, generation int64) (llmbroker.Credential, error) {
	begin := func(ctx context.Context, token string) (store.LLMAccount, error) {
		return c.Store.BeginOperatorLLMMint(ctx, token, executionID, generation)
	}
	record := func(ctx context.Context, token, keyID string) error {
		return c.Store.RecordOperatorLLMIssued(ctx, token, keyID, executionID, generation)
	}
	return c.issue(ctx, runToken, begin, record)
}

func (c *LLMControl) issue(ctx context.Context, runToken string, begin func(context.Context, string) (store.LLMAccount, error), record func(context.Context, string, string) error) (llmbroker.Credential, error) {
	a, err := begin(ctx, runToken)
	if err != nil {
		return llmbroker.Credential{}, err
	}
	cred, err := c.Broker.Mint(ctx, llmbroker.MintRequest{RunToken: runToken, BudgetUSD: a.Authorized, Models: a.Models, TTL: time.Duration(a.TTLSeconds) * time.Second,
		TeamID: a.GatewayTeamID, MCPAccessGroups: a.MCPAccessGroups})
	if err != nil {
		c.markUnknown(runToken)
		return llmbroker.Credential{}, fmt.Errorf("credential issuance unresolved; reconciliation required")
	}
	keyID := sha256.Sum256([]byte(cred.APIKey))
	if cred.APIKey == "" || cred.Alias != a.Alias {
		c.markUnknown(runToken)
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Broker.Revoke(cleanup, cred)
		return llmbroker.Credential{}, fmt.Errorf("gateway returned an invalid credential")
	}
	if err := record(ctx, runToken, hex.EncodeToString(keyID[:])); err != nil {
		c.markUnknown(runToken)
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Broker.Revoke(cleanup, cred)
		return llmbroker.Credential{}, fmt.Errorf("credential recording unresolved; reconciliation required")
	}
	return cred, nil
}

func (c *LLMControl) markUnknown(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Store.MarkLLMUnknown(ctx, token)
}

func (c *LLMControl) Block(ctx context.Context, runToken string) error {
	a, err := c.Store.LLMAccount(ctx, runToken)
	if err != nil {
		return err
	}
	if a.State == "blocked" || a.State == "reconciled" {
		return nil
	}
	if a.State == "reserved" {
		blocked, err := c.Store.BlockUnissuedLLMAccount(ctx, runToken)
		if err != nil || blocked {
			return err
		}
	}
	if err := c.Broker.RevokeForRun(ctx, runToken); err != nil {
		return err
	}
	spend, err := c.Broker.SpendForRun(ctx, runToken)
	if err != nil {
		if evidence, gone := c.keysGone(ctx, a); gone {
			return c.Store.RecordLLMKeyGone(ctx, runToken, evidence)
		}
		c.markUnknown(runToken)
		return fmt.Errorf("gateway accounting identity unavailable; reconciliation required")
	}
	return c.Store.RecordLLMBlocked(ctx, runToken, &spend)
}

func (c *LLMControl) keysGone(ctx context.Context, a store.LLMAccount) (string, bool) {
	prober, ok := c.Broker.(llmbroker.KeyProber)
	if !ok {
		return "", false
	}
	gone, err := prober.RunKeysGone(ctx, a.RunToken, []string{a.GatewayKeyID})
	if err != nil || !gone {
		return "", false
	}
	return fmt.Sprintf("litellm:key-absent alias=%s prior-state=%s recorded-key=%t read-at=%s",
		a.Alias, a.State, a.GatewayKeyID != "", time.Now().UTC().Format(time.RFC3339)), true
}

func (c *LLMControl) Spend(ctx context.Context, runToken string) (float64, error) {
	if _, err := c.Store.LLMAccount(ctx, runToken); err != nil {
		return 0, err
	}
	spend, err := c.Broker.SpendForRun(ctx, runToken)
	if err != nil {
		return 0, err
	}
	if err := c.Store.RecordLLMObserved(ctx, runToken, spend); err != nil {
		return 0, err
	}
	return spend, nil
}

// Live reads what the gateway has recorded so far for a Run's keys, from the
// spend logs settlement reads. It records nothing, so a reading never moves
// the Run's budget or settlement.
func (c *LLMControl) Live(ctx context.Context, runToken string) (store.LiveUsage, error) {
	a, err := c.Store.LLMAccount(ctx, runToken)
	if err != nil {
		return store.LiveUsage{}, err
	}
	settler, ok := c.Broker.(llmbroker.Settler)
	if !ok {
		return store.LiveUsage{}, fmt.Errorf("gateway has no durable spend source")
	}
	spend, err := settler.SettledSpendForRun(ctx, runToken, []string{a.GatewayKeyID})
	if err != nil {
		return store.LiveUsage{}, err
	}
	return store.LiveUsage{CostUSD: spend.USD, InputTokens: spend.InputTokens, OutputTokens: spend.OutputTokens}, nil
}

// Settle reconciles a finished Run's account as the trusted controller-side
// caller. An account with no durable sign of a mint settles at zero on that
// evidence; a blocked account settles at the gateway's spend-log total for
// its keys, read after the account has stayed unchanged, and records the
// tokens and models from the same entries as the Run's usage. A broker that
// cannot read durable spend never settles a minted account. A spend-log
// settlement stays provisional for CorrectionWindow, and a minted account
// whose spend logs have no entries settles at zero with its cost recorded as
// unknown.
func (c *LLMControl) Settle(ctx context.Context, a store.UnsettledLLMAccount) error {
	if !a.MintBegan {
		return c.Store.ReconcileLLMAccount(ctx, a.RunToken, 0, "ploegd:mint-never-began account="+a.State+" alias="+a.Alias)
	}
	if a.State != "blocked" {
		return store.ErrLLMAccountState
	}
	_, err := c.settleFromSpendLogs(ctx, a)
	return err
}

// Correct reads the spend logs of a reconciled account inside its correction
// window again, as CorrectableLLMAccounts offers it. A higher total is charged
// as an adjustment; the first settlement stays recorded. It reports whether
// anything changed.
func (c *LLMControl) Correct(ctx context.Context, a store.UnsettledLLMAccount) (bool, error) {
	if a.State != "reconciled" {
		return false, store.ErrLLMAccountState
	}
	return c.settleFromSpendLogs(ctx, a)
}

func (c *LLMControl) settleFromSpendLogs(ctx context.Context, a store.UnsettledLLMAccount) (bool, error) {
	settler, ok := c.Broker.(llmbroker.Settler)
	if !ok {
		return false, fmt.Errorf("gateway has no durable spend source; reconciliation required")
	}
	spend, err := settler.SettledSpendForRun(ctx, a.RunToken, []string{a.GatewayKeyID})
	if err != nil {
		return false, fmt.Errorf("gateway spend logs unavailable: %w; reconciliation required", err)
	}
	source := "litellm:spend-logs"
	if spend.ByAlias {
		source = "litellm:spend-logs-by-alias"
	}
	evidence := fmt.Sprintf("%s alias=%s keys=%d entries=%d usd=%s unchanged-since=%s read-at=%s",
		source, a.Alias, spend.Keys, spend.Entries, strconv.FormatFloat(spend.USD, 'f', -1, 64), a.QuietSince.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if spend.USD < a.ObservedSpend {
		evidence += " settled-at-observation=" + strconv.FormatFloat(a.ObservedSpend, 'f', -1, 64)
		spend.USD = a.ObservedSpend
	}
	usage := store.SettledUsage{InputTokens: spend.InputTokens, OutputTokens: spend.OutputTokens, Models: spend.Models}
	for _, m := range spend.ByModel {
		usage.ByModel = append(usage.ByModel, store.ModelUsage{Model: m.Model, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CostUSD: m.USD})
	}
	return c.Store.SettleLLMAccount(ctx, a.RunToken, store.LLMSettlement{Spend: spend.USD, Evidence: evidence, Usage: &usage,
		CostKnown: spend.Entries > 0, CorrectionWindow: c.CorrectionWindow})
}

func (s *Server) RegisterLLMControl(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/runs/{token}/llm/credential", func(w http.ResponseWriter, r *http.Request) {
		if s.LLMControl == nil {
			http.Error(w, "managed inference unavailable", http.StatusServiceUnavailable)
			return
		}
		cred, err := s.LLMControl.Issue(r.Context(), r.PathValue("token"))
		if err != nil {
			llmControlError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, cred)
	})
	mux.HandleFunc("POST /api/v1/runs/{token}/llm/block", func(w http.ResponseWriter, r *http.Request) {
		if s.LLMControl == nil {
			http.Error(w, "managed inference unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := s.LLMControl.Block(r.Context(), r.PathValue("token")); err != nil {
			llmControlError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/runs/{token}/llm/spend", func(w http.ResponseWriter, r *http.Request) {
		if s.LLMControl == nil {
			http.Error(w, "managed inference unavailable", http.StatusServiceUnavailable)
			return
		}
		spend, err := s.LLMControl.Spend(r.Context(), r.PathValue("token"))
		if err != nil {
			llmControlError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"costUsd": spend, "status": "provisional"})
	})
}

func llmControlError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	if errors.Is(err, store.ErrLLMAccountState) {
		status = http.StatusConflict
	}
	http.Error(w, "inference operation unresolved; reconciliation required", status)
}

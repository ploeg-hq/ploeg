package llmbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
)

// LiteLLM implements Broker and Sweeper against the LiteLLM proxy admin
// API. The alias invariant ("ploeg-" + first 12 hex of the run token,
// Grafana joins on it) lives in pkg/litellm — this type only applies it.
type LiteLLM struct {
	cli *litellm.Client
}

func NewLiteLLM(cli *litellm.Client) *LiteLLM { return &LiteLLM{cli: cli} }

var _ Broker = (*LiteLLM)(nil)
var _ Sweeper = (*LiteLLM)(nil)
var _ Metered = (*LiteLLM)(nil)
var _ Settler = (*LiteLLM)(nil)
var _ KeyProber = (*LiteLLM)(nil)

// Spend returns provisional gateway usage for a retained accounting identity.
func (b *LiteLLM) Spend(ctx context.Context, cred Credential) (float64, error) {
	if cred.APIKey == "" {
		return 0, fmt.Errorf("no credential to meter")
	}
	keyID := sha256.Sum256([]byte(cred.APIKey))
	return b.cli.KeySpend(ctx, hex.EncodeToString(keyID[:]))
}

func (b *LiteLLM) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	alias := litellm.Alias(req.RunToken)
	if alias == "" {
		return Credential{}, fmt.Errorf("run token too short for key alias")
	}
	// Fail closed. MaxBudget is omitempty, so a zero budget does not mint a
	// zero-spend key — it mints an UNCAPPED one, silently. The only way that
	// value reaches here is a misconfiguration (an unset or unparseable
	// LITELLM_KEY_BUDGET, a team with no cap and no budget), and an
	// unattended agent with an unlimited credential is the one outcome this
	// package exists to prevent.
	if req.BudgetUSD <= 0 || math.IsNaN(req.BudgetUSD) || math.IsInf(req.BudgetUSD, 0) {
		return Credential{}, fmt.Errorf("refusing to mint an uncapped key: budget is %v", req.BudgetUSD)
	}
	// An MCP grant without a team is bounded only by the master key, which
	// can grant every group the gateway has. The team is the boundary, so a
	// grant without one is refused here rather than minted wide (ADR-0078).
	if len(req.MCPAccessGroups) > 0 && req.TeamID == "" {
		return Credential{}, fmt.Errorf("refusing to grant MCP access groups without a gateway team")
	}
	mint := litellm.MintRequest{
		KeyType:   "llm_api",
		KeyAlias:  alias,
		MaxBudget: req.BudgetUSD,
		Models:    req.Models,
		Duration:  ttlString(req.TTL),
		TeamID:    req.TeamID,
	}
	if len(req.MCPAccessGroups) > 0 {
		mint.ObjectPermission = &litellm.ObjectPermission{MCPAccessGroups: req.MCPAccessGroups}
	}
	key, err := b.cli.Mint(ctx, mint)
	if err != nil {
		return Credential{}, err
	}
	cred := Credential{APIKey: key, Alias: alias}
	if len(req.MCPAccessGroups) > 0 {
		cred.MCPAccessGroups = append([]string(nil), req.MCPAccessGroups...)
	}
	return cred, nil
}

// ttlString renders a TTL in the duration format LiteLLM parses ("30s",
// "30m", "30h", "30d"). Seconds, so nothing is lost to rounding — a 90m TTL
// sent as "1h" would expire half an hour early. Empty for a non-positive
// TTL, which leaves the key without an expiry; that is the caller's choice
// to make explicitly, not this function's to invent.
func ttlString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
}

func (b *LiteLLM) Revoke(ctx context.Context, cred Credential) error {
	if cred.APIKey == "" {
		return nil
	}
	return b.cli.BlockKey(ctx, cred.APIKey)
}

// RevokeForRun blocks the run's keys by exact alias, preserving their accounting identities.
func (b *LiteLLM) RevokeForRun(ctx context.Context, runToken string) error {
	alias := litellm.Alias(runToken)
	if alias == "" {
		return fmt.Errorf("run token too short for key alias")
	}
	keys, err := b.cli.ListKeys(ctx, alias)
	if err != nil {
		return fmt.Errorf("list keys for %s: %w", alias, err)
	}
	tokens := make([]string, 0, len(keys))
	for _, k := range keys {
		if k.KeyAlias == alias && !k.Blocked {
			tokens = append(tokens, k.Token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	for _, token := range tokens {
		if err := b.cli.BlockKey(ctx, token); err != nil {
			return err
		}
	}
	return nil
}

// SweepOrphans blocks every active ploeg-* key without an unfinished run.
func (b *LiteLLM) SweepOrphans(ctx context.Context, aliveRunTokens []string) (int, error) {
	keys, err := b.cli.ListKeys(ctx, litellm.AliasPrefix)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	alive := make(map[string]struct{}, len(aliveRunTokens))
	for _, tok := range aliveRunTokens {
		if alias := litellm.Alias(tok); alias != "" {
			alive[alias] = struct{}{}
		}
	}
	var stale []string
	for _, k := range keys {
		if _, live := alive[k.KeyAlias]; !live && !k.Blocked {
			stale = append(stale, k.Token)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}
	for i, token := range stale {
		if err := b.cli.BlockKey(ctx, token); err != nil {
			return i, err
		}
	}
	return len(stale), nil
}

// SettledSpendForRun sums the spend logs of every hashed key token known for
// the run: the recorded key identities plus any live key carrying its alias.
func (b *LiteLLM) SettledSpendForRun(ctx context.Context, runToken string, keyIDs []string) (SettledSpend, error) {
	alias := litellm.Alias(runToken)
	if alias == "" {
		return SettledSpend{}, fmt.Errorf("invalid run identity")
	}
	keys, err := b.cli.ListKeys(ctx, alias)
	if err != nil {
		return SettledSpend{}, err
	}
	tokens := map[string]struct{}{}
	for _, id := range keyIDs {
		if id != "" {
			tokens[id] = struct{}{}
		}
	}
	for _, key := range keys {
		if key.KeyAlias == alias && key.Token != "" {
			tokens[key.Token] = struct{}{}
		}
	}
	if len(tokens) == 0 {
		return SettledSpend{}, fmt.Errorf("gateway accounting identity unavailable")
	}
	settled := SettledSpend{Keys: len(tokens)}
	models := map[string]*ModelSpend{}
	for token := range tokens {
		logs, err := b.cli.SpendLogs(ctx, token)
		if err != nil {
			return SettledSpend{}, err
		}
		settled.USD += logs.USD
		settled.Entries += logs.Entries
		settled.InputTokens += logs.PromptTokens
		settled.OutputTokens += logs.CompletionTokens
		for _, usage := range logs.ByModel {
			m := models[usage.Model]
			if m == nil {
				m = &ModelSpend{Model: usage.Model}
				models[usage.Model] = m
			}
			m.USD += usage.USD
			m.InputTokens += usage.PromptTokens
			m.OutputTokens += usage.CompletionTokens
		}
	}
	settled.Models = make([]string, 0, len(models))
	for model := range models {
		settled.Models = append(settled.Models, model)
	}
	sort.Strings(settled.Models)
	settled.ByModel = make([]ModelSpend, 0, len(models))
	for _, model := range settled.Models {
		settled.ByModel = append(settled.ByModel, *models[model])
	}
	return settled, nil
}

// RunKeysGone reports true when /key/list carries no key with the run's alias
// and /key/info answers 404 for every recorded key identity.
func (b *LiteLLM) RunKeysGone(ctx context.Context, runToken string, keyIDs []string) (bool, error) {
	alias := litellm.Alias(runToken)
	if alias == "" {
		return false, fmt.Errorf("invalid run identity")
	}
	keys, err := b.cli.ListKeys(ctx, alias)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		if key.KeyAlias == alias {
			return false, nil
		}
	}
	for _, id := range keyIDs {
		if id == "" {
			continue
		}
		exists, err := b.cli.KeyExists(ctx, id)
		if err != nil || exists {
			return false, err
		}
	}
	return true, nil
}

func (b *LiteLLM) SpendForRun(ctx context.Context, runToken string) (float64, error) {
	alias := litellm.Alias(runToken)
	if alias == "" {
		return 0, fmt.Errorf("invalid run identity")
	}
	keys, err := b.cli.ListKeys(ctx, alias)
	if err != nil {
		return 0, err
	}
	var total float64
	found := false
	for _, key := range keys {
		if key.KeyAlias != alias {
			continue
		}
		spend, err := b.cli.KeySpend(ctx, key.Token)
		if err != nil {
			return 0, err
		}
		total += spend
		found = true
	}
	if !found {
		return 0, fmt.Errorf("gateway accounting identity unavailable")
	}
	return total, nil
}

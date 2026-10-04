// Package litellm wraps the LiteLLM proxy admin API for per-run key
// lifecycle (mint + revoke, list + batch delete). Importers should create
// a single Client per process and reuse it for sweeper operations.
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AliasPrefix is the fixed prefix for every per-run LiteLLM key alias.
// Grafana joins spend↔run↔ticket on key_alias matching "ploeg-<12hex>".
const AliasPrefix = "ploeg-"

// Alias returns the LiteLLM key alias for a run token: "ploeg-" + first 12
// hex characters of the token. Returns "" when the token is too short; the
// caller should skip the operation (best-effort cleanup must never crash).
func Alias(runToken string) string {
	if len(runToken) < 12 {
		return ""
	}
	return AliasPrefix + runToken[:12]
}

// Client talks to a LiteLLM proxy admin API.
type Client struct {
	baseURL   string
	masterKey string
	httpCli   *http.Client
}

// NewClient returns a Client pointing at the given LiteLLM proxy base URL
// (e.g. "http://litellm:4000") and authenticating with the admin master key.
func NewClient(baseURL, masterKey string) *Client {
	return &Client{
		baseURL:   baseURL,
		masterKey: masterKey,
		httpCli: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// MintRequest is the JSON body for POST /key/generate.
type MintRequest struct {
	KeyType   string   `json:"key_type,omitempty"`
	KeyAlias  string   `json:"key_alias"`
	MaxBudget float64  `json:"max_budget,omitempty"`
	Models    []string `json:"models,omitempty"`
	// Duration is LiteLLM's key TTL, a duration string it parses itself
	// ("30s", "30m", "30h", "30d"). This field used to be `max_hours_ttl`,
	// which is not a LiteLLM field at all: GenerateKeyRequest ignores unknown
	// keys, so every key ever minted by Ploeg had NO expiry while three
	// separate comments called the TTL "the backstop". A revoke that failed
	// left an immortal budgeted credential — observed on 2026-07-24..27, when
	// eight keys for finished runs accumulated and stayed live.
	Duration string `json:"duration,omitempty"`
}

// MintResponse is the subset of the /key/generate response we need.
type MintResponse struct {
	Key string `json:"key"`
}

// Mint creates a new per-run key and returns its string value.
func (c *Client) Mint(ctx context.Context, req MintRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("litellm: marshal mint request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/key/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("litellm: invalid mint endpoint")
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.masterKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpCli.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("litellm: mint request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("litellm: mint request got HTTP %d", resp.StatusCode)
	}

	var mr MintResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&mr); err != nil {
		return "", fmt.Errorf("litellm: invalid mint response")
	}
	if mr.Key == "" {
		return "", fmt.Errorf("litellm: mint response returned empty key")
	}
	return mr.Key, nil
}

// Revoke deletes a previously-minted key via POST /key/delete.
// Errors are logged by the caller; MintRequest.Duration is the backstop, and
// the periodic orphan sweep is the one after that.
func (c *Client) Revoke(ctx context.Context, key string) error {
	return c.DeleteKeys(ctx, []string{key})
}

// keyInfoResponse is the subset of GET /key/info we need. `info.spend` is the
// proxy's own running total for that key, which is what makes it usable as an
// enforcement figure: it is the gateway's accounting, not a number the agent
// reported about itself.
type keyInfoResponse struct {
	Info struct {
		Spend     *float64 `json:"spend"`
		MaxBudget float64  `json:"max_budget"`
	} `json:"info"`
}

// KeySpend returns what a key has spent so far. Only meaningful while the key
// exists — call it before Revoke.
func (c *Client) KeySpend(ctx context.Context, key string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/key/info?key="+url.QueryEscape(key), nil)
	if err != nil {
		return 0, fmt.Errorf("litellm: invalid key info endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return 0, fmt.Errorf("litellm: key info request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("litellm: key info got HTTP %d", resp.StatusCode)
	}
	var ki keyInfoResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ki); err != nil {
		return 0, fmt.Errorf("litellm: invalid key info response")
	}
	if ki.Info.Spend == nil || *ki.Info.Spend < 0 || math.IsNaN(*ki.Info.Spend) || math.IsInf(*ki.Info.Spend, 0) {
		return 0, fmt.Errorf("litellm: spend is unavailable or invalid")
	}
	return *ki.Info.Spend, nil
}

// KeyExists reports whether the gateway still holds a key, asked through
// GET /key/info with either the plaintext key or its hashed token. Only a 404
// answers false; any other failure is an error, so an unreachable gateway is
// never read as a deleted key.
func (c *Client) KeyExists(ctx context.Context, key string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/key/info?key="+url.QueryEscape(key), nil)
	if err != nil {
		return false, fmt.Errorf("litellm: invalid key info endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return false, fmt.Errorf("litellm: key info request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("litellm: key info got HTTP %d", resp.StatusCode)
	}
}

// SpendLogTotal sums the gateway's spend log entries for a hashed key token
// through GET /spend/logs?api_key=. Spend logs outlive the key row, whose
// running total is written asynchronously and disappears when a key is
// deleted, so they are the settlement source. It also returns how many
// entries were summed.
func (c *Client) SpendLogTotal(ctx context.Context, token string) (float64, int, error) {
	summary, err := c.SpendLogs(ctx, token)
	if err != nil {
		return 0, 0, err
	}
	return summary.USD, summary.Entries, nil
}

// SpendLogSummary is what one key's spend log entries add up to. Token
// counts absent from an entry count as zero; Models is sorted and unique, and
// ByModel splits the same totals per model in the same order.
type SpendLogSummary struct {
	USD              float64
	Entries          int
	PromptTokens     int64
	CompletionTokens int64
	Models           []string
	ByModel          []ModelUsage
}

// ModelUsage is the share of a key's spend log entries that name one model.
// Entries without a model are counted in the summary's totals only.
type ModelUsage struct {
	Model            string
	USD              float64
	Entries          int
	PromptTokens     int64
	CompletionTokens int64
}

// SpendLogs reads the same entries as SpendLogTotal and also aggregates their
// prompt and completion tokens and the models they name. A missing or invalid
// spend fails the read; a missing or invalid token count does not.
func (c *Client) SpendLogs(ctx context.Context, token string) (SpendLogSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/spend/logs?api_key="+url.QueryEscape(token), nil)
	if err != nil {
		return SpendLogSummary{}, fmt.Errorf("litellm: invalid spend logs endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return SpendLogSummary{}, fmt.Errorf("litellm: spend logs request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SpendLogSummary{}, fmt.Errorf("litellm: spend logs got HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<20))
	if open, err := decoder.Token(); err != nil || open != json.Delim('[') {
		return SpendLogSummary{}, fmt.Errorf("litellm: invalid spend logs response")
	}
	var summary SpendLogSummary
	models := map[string]*ModelUsage{}
	for decoder.More() {
		var entry struct {
			APIKey           string   `json:"api_key"`
			Spend            *float64 `json:"spend"`
			Model            string   `json:"model"`
			PromptTokens     *float64 `json:"prompt_tokens"`
			CompletionTokens *float64 `json:"completion_tokens"`
		}
		if err := decoder.Decode(&entry); err != nil {
			return SpendLogSummary{}, fmt.Errorf("litellm: invalid spend logs response")
		}
		if entry.APIKey != token || entry.Spend == nil || *entry.Spend < 0 || math.IsNaN(*entry.Spend) || math.IsInf(*entry.Spend, 0) {
			return SpendLogSummary{}, fmt.Errorf("litellm: spend log entry is unavailable or invalid")
		}
		prompt, completion := tokenCount(entry.PromptTokens), tokenCount(entry.CompletionTokens)
		summary.USD += *entry.Spend
		summary.Entries++
		summary.PromptTokens += prompt
		summary.CompletionTokens += completion
		if model := strings.TrimSpace(entry.Model); model != "" {
			m := models[model]
			if m == nil {
				m = &ModelUsage{Model: model}
				models[model] = m
			}
			m.USD += *entry.Spend
			m.Entries++
			m.PromptTokens += prompt
			m.CompletionTokens += completion
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
		return SpendLogSummary{}, fmt.Errorf("litellm: invalid spend logs response")
	}
	summary.Models = make([]string, 0, len(models))
	for model := range models {
		summary.Models = append(summary.Models, model)
	}
	sort.Strings(summary.Models)
	summary.ByModel = make([]ModelUsage, 0, len(models))
	for _, model := range summary.Models {
		summary.ByModel = append(summary.ByModel, *models[model])
	}
	return summary, nil
}

func tokenCount(v *float64) int64 {
	if v == nil || *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) || *v > math.MaxInt32 {
		return 0
	}
	return int64(math.Round(*v))
}

// KeyInfo is a single entry from the /key/list response (with
// return_full_object=true).
type KeyInfo struct {
	Token    string `json:"token"`
	KeyAlias string `json:"key_alias"`
	Blocked  bool   `json:"blocked"`
}

func (c *Client) BlockKey(ctx context.Context, key string) error {
	body, err := json.Marshal(map[string]string{"key": key})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/key/block", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("litellm: invalid block endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return fmt.Errorf("litellm: block request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("litellm: block got HTTP %d", resp.StatusCode)
	}
	var result struct {
		Blocked bool `json:"blocked"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || !result.Blocked {
		return fmt.Errorf("litellm: gateway did not confirm key blocked")
	}
	return nil
}

// listKeysResponse is the full /key/list response shape.
type listKeysResponse struct {
	Keys        []KeyInfo `json:"keys"`
	TotalCount  int       `json:"total_count"`
	TotalPages  int       `json:"total_pages"`
	CurrentPage int       `json:"current_page"`
}

// ListKeys lists all keys from the LiteLLM proxy. When prefix is non-empty,
// results are filtered client-side to keys whose key_alias starts with the
// given prefix. The method paginates through all pages.
func (c *Client) ListKeys(ctx context.Context, prefix string) ([]KeyInfo, error) {
	var all []KeyInfo
	page := 1
	for {
		url := fmt.Sprintf("%s/key/list?return_full_object=true&size=100&page=%d", c.baseURL, page)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("litellm: invalid list endpoint")
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.masterKey)

		resp, err := c.httpCli.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("litellm: list request failed")
		}

		var lr listKeysResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&lr); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("litellm: invalid list response")
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("litellm: list request got HTTP %d", resp.StatusCode)
		}

		for _, k := range lr.Keys {
			if prefix == "" || strings.HasPrefix(k.KeyAlias, prefix) {
				all = append(all, k)
			}
		}

		if page >= lr.TotalPages {
			break
		}
		page++
	}
	return all, nil
}

// DeleteKeys deletes one or more LiteLLM keys by their hashed token values
// (the "token" field from /key/list). Sending tokens for already-deleted keys
// is idempotent: the proxy returns HTTP 200 (deletes what exists, echoes the
// rest) or HTTP 404 when none exist — both are treated as success here.
func (c *Client) DeleteKeys(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"keys": tokens})
	if err != nil {
		return fmt.Errorf("litellm: marshal delete request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/key/delete", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("litellm: invalid delete endpoint")
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.masterKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpCli.Do(httpReq)
	if err != nil {
		return fmt.Errorf("litellm: delete request failed")
	}
	defer resp.Body.Close()

	// 200 = at least some deleted; 404 = none found (idempotent).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("litellm: delete request got HTTP %d", resp.StatusCode)
	}
	return nil
}

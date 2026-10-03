package forgebroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// namePrefix marks every token this package mints, so the boot sweep can tell
// ours from a human's without a registry. Same trick as the LiteLLM alias.
const namePrefix = "ploeg-run-"

// Forgejo mints Forgejo access tokens for the bot through
// /api/v1/users/{bot}/tokens.
//
// Forgejo serves no admin endpoint for another user's tokens and refuses token
// authentication on the tokens API, so the broker signs in to it with the
// bot's own username and password. Each token is limited to the
// `write:repository` scope and, through the token's repository list, to the
// Run's own repository: git and the repository API answer 403 for every other
// repository, even ones the bot can reach.
type Forgejo struct {
	// BaseURL is the instance root.
	BaseURL string
	// Bot is the forge user whose tokens are minted (agent-builder).
	Bot string
	// Password is Bot's password. It lives ONLY in ploegd, never in a worker
	// pod (R6), the same way LITELLM_MASTER_KEY is held.
	Password string
	HC       *http.Client
}

func (f *Forgejo) client() *http.Client {
	if f.HC != nil {
		return f.HC
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *Forgejo) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(f.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(f.Bot, f.Password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("forgejo token API: %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// tokenName encodes the run and the repository it was minted for. Readable in
// the forge UI and parseable by the sweep; the run prefix is the same 12 hex
// that joins spend to ticket in Grafana.
func tokenName(runToken, owner, repo string) string {
	return fmt.Sprintf("%s%s-%s-%s", namePrefix, runPrefix(runToken), owner, repo)
}

func runPrefix(runToken string) string {
	if len(runToken) > 12 {
		return runToken[:12]
	}
	return runToken
}

func (f *Forgejo) tokensPath() string {
	return "/api/v1/users/" + url.PathEscape(f.Bot) + "/tokens"
}

func (f *Forgejo) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	if req.Owner == "" || req.Repo == "" {
		return Credential{}, fmt.Errorf("forgebroker: a per-run token needs a repository")
	}
	name := tokenName(req.RunToken, req.Owner, req.Repo)
	var out struct {
		ID     int64    `json:"id"`
		Name   string   `json:"name"`
		Sha1   string   `json:"sha1"`
		Scopes []string `json:"scopes"`
	}
	body := map[string]any{
		"name":         name,
		"scopes":       []string{"write:repository"},
		"repositories": []map[string]string{{"owner": req.Owner, "name": req.Repo}},
	}
	if err := f.do(ctx, http.MethodPost,
		f.tokensPath(), body, &out); err != nil {
		return Credential{}, err
	}
	if out.Sha1 == "" {
		return Credential{}, fmt.Errorf("forgebroker: forge returned no token for %q", name)
	}
	return Credential{ID: fmt.Sprint(out.ID), Token: out.Sha1, Name: name}, nil
}

func (f *Forgejo) Revoke(ctx context.Context, cred Credential) error {
	target := cred.Name
	if target == "" {
		target = cred.ID
	}
	if target == "" {
		return nil // nothing was minted (Static, or a mint that failed)
	}
	err := f.do(ctx, http.MethodDelete,
		f.tokensPath()+"/"+url.PathEscape(target), nil, nil)
	// An already-deleted token is the desired end state, not a failure: the
	// sweeper and the worker's defer both revoke, and they race by design.
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		return nil
	}
	return err
}

// RevokeByID satisfies Sweeper. Forgejo deletes by name or id; the lease row
// stores whichever the mint returned.
func (f *Forgejo) RevokeByID(ctx context.Context, id string) error {
	return f.Revoke(ctx, Credential{ID: id})
}

const sweepPageSize = 50

type listedToken struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (f *Forgejo) listTokens(ctx context.Context) ([]listedToken, error) {
	var all []listedToken
	for page := 1; ; page++ {
		var batch []listedToken
		path := fmt.Sprintf("%s?page=%d&limit=%d", f.tokensPath(), page, sweepPageSize)
		if err := f.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < sweepPageSize {
			return all, nil
		}
	}
}

// SweepOrphans revokes every ploeg-minted token whose Run is not in leased,
// paging through the whole token list. It retries every revocation that
// failed and reaps a token minted by a ploegd that died before recording it.
// A token it cannot revoke does not stop the rest; the errors are joined.
// Tokens this package did not mint are never touched.
func (f *Forgejo) SweepOrphans(ctx context.Context, leased func(context.Context) ([]string, error)) (int, error) {
	tokens, err := f.listTokens(ctx)
	if err != nil {
		return 0, err
	}
	runTokens, err := leased(ctx)
	if err != nil {
		return 0, err
	}
	held := make(map[string]bool, len(runTokens))
	for _, rt := range runTokens {
		held[runPrefix(rt)] = true
	}
	revoked := 0
	var errs []error
	for _, t := range tokens {
		run, ours := mintedFor(t.Name)
		if !ours || held[run] {
			continue
		}
		if err := f.Revoke(ctx, Credential{ID: fmt.Sprint(t.ID), Name: t.Name}); err != nil {
			errs = append(errs, err)
			continue
		}
		revoked++
	}
	return revoked, errors.Join(errs...)
}

func mintedFor(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return "", false
	}
	run, _, ok := strings.Cut(rest, "-")
	return run, ok && run != ""
}

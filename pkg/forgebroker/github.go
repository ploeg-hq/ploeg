package forgebroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapp"
)

// GitHubAppIDPrefix marks the credential ids GitHubApp mints, so a Router
// can send a revocation back to it.
const GitHubAppIDPrefix = "github-app:"

// GitHubApp mints a GitHub App installation token per writing Run, limited to
// the Run's repository and to githubapp.WriterPermissions (ADR-0086).
//
// GitHub lists no installation tokens and revokes one only when presented
// with the token itself, so the broker keeps every token it minted in memory
// under an id that names its Run. Revoke and the sweep work from that map.
// A token whose ploegd restarted before revoking it is no longer reachable;
// GitHub ends it at its one-hour expiry, which is the bound this broker
// guarantees in place of Forgejo's revoke-only one.
type GitHubApp struct {
	App         *githubapp.Minter
	Permissions githubapp.Permissions

	mu     sync.Mutex
	minted map[string]githubMinted
}

type githubMinted struct {
	token githubapp.Token
	run   string
}

func (g *GitHubApp) now() time.Time {
	if g.App.Now != nil {
		return g.App.Now()
	}
	return time.Now()
}

// Mint issues an installation token for req's repository alone.
func (g *GitHubApp) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	if req.Owner == "" || req.Repo == "" {
		return Credential{}, errors.New("forgebroker: a per-run token needs a repository")
	}
	perms := g.Permissions
	if perms == nil {
		perms = githubapp.WriterPermissions()
	}
	tok, err := g.App.Mint(ctx, req.Owner, req.Repo, perms)
	if err != nil {
		return Credential{}, err
	}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		_ = g.App.Revoke(context.WithoutCancel(ctx), tok.Value)
		return Credential{}, err
	}
	run := runPrefix(req.RunToken)
	id := GitHubAppIDPrefix + run + "-" + hex.EncodeToString(nonce)
	g.mu.Lock()
	if g.minted == nil {
		g.minted = map[string]githubMinted{}
	}
	g.minted[id] = githubMinted{token: tok, run: run}
	g.mu.Unlock()
	return Credential{ID: id, Token: tok.Value, Name: tokenName(req.RunToken, req.Owner, req.Repo), ExpiresAt: tok.ExpiresAt}, nil
}

// Revoke ends the token cred names. An id this broker no longer holds was
// revoked already or belonged to an earlier ploegd, and GitHub's expiry ends
// it; that is not an error.
func (g *GitHubApp) Revoke(ctx context.Context, cred Credential) error {
	token := cred.Token
	if cred.ID != "" {
		g.mu.Lock()
		if m, ok := g.minted[cred.ID]; ok {
			token = m.token.Value
		}
		g.mu.Unlock()
	}
	if token == "" {
		return nil
	}
	if err := g.App.Revoke(ctx, token); err != nil {
		return fmt.Errorf("forgebroker: revoke github token %s: %w", cred.ID, err)
	}
	if cred.ID != "" {
		g.mu.Lock()
		delete(g.minted, cred.ID)
		g.mu.Unlock()
	}
	return nil
}

// RevokeByID satisfies Sweeper.
func (g *GitHubApp) RevokeByID(ctx context.Context, id string) error {
	return g.Revoke(ctx, Credential{ID: id})
}

// SweepOrphans revokes every token this broker holds whose Run holds no
// Lease, and forgets tokens GitHub has expired.
func (g *GitHubApp) SweepOrphans(ctx context.Context, leased func(context.Context) ([]string, error)) (int, error) {
	g.mu.Lock()
	snapshot := make(map[string]githubMinted, len(g.minted))
	for id, m := range g.minted {
		snapshot[id] = m
	}
	g.mu.Unlock()
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
	for id, m := range snapshot {
		if !m.token.ExpiresAt.IsZero() && g.now().After(m.token.ExpiresAt) {
			g.mu.Lock()
			delete(g.minted, id)
			g.mu.Unlock()
			continue
		}
		if held[m.run] {
			continue
		}
		if err := g.Revoke(ctx, Credential{ID: id}); err != nil {
			errs = append(errs, err)
			continue
		}
		revoked++
	}
	return revoked, errors.Join(errs...)
}

// Router sends a Run's mint to the broker of its forge and a revocation to
// the broker that minted the credential: GitHub for the forge ids in
// GitHubForges, Default for every other.
type Router struct {
	Default        Broker
	DefaultSweeper Sweeper
	GitHub         *GitHubApp
	// GitHubForges are the forge ids whose Runs get GitHub App tokens.
	GitHubForges []string
	// DefaultForge is the forge id of a Work Target that names none.
	DefaultForge string
}

func (r *Router) github(forge string) bool {
	if forge == "" {
		forge = r.DefaultForge
	}
	for _, f := range r.GitHubForges {
		if strings.EqualFold(f, forge) {
			return r.GitHub != nil
		}
	}
	return false
}

// Mint mints with the broker of req.Forge.
func (r *Router) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	if r.github(req.Forge) {
		return r.GitHub.Mint(ctx, req)
	}
	if r.Default == nil {
		return Credential{}, nil
	}
	return r.Default.Mint(ctx, req)
}

// Revoke revokes with the broker that minted cred.
func (r *Router) Revoke(ctx context.Context, cred Credential) error {
	if r.GitHub != nil && strings.HasPrefix(cred.ID, GitHubAppIDPrefix) {
		return r.GitHub.Revoke(ctx, cred)
	}
	if r.Default == nil {
		return nil
	}
	return r.Default.Revoke(ctx, cred)
}

// RevokeByID revokes with the sweeper that minted id.
func (r *Router) RevokeByID(ctx context.Context, id string) error {
	if r.GitHub != nil && strings.HasPrefix(id, GitHubAppIDPrefix) {
		return r.GitHub.RevokeByID(ctx, id)
	}
	if r.DefaultSweeper == nil {
		return nil
	}
	return r.DefaultSweeper.RevokeByID(ctx, id)
}

// SweepOrphans sweeps every broker the router holds.
func (r *Router) SweepOrphans(ctx context.Context, leased func(context.Context) ([]string, error)) (int, error) {
	total := 0
	var errs []error
	if r.DefaultSweeper != nil {
		n, err := r.DefaultSweeper.SweepOrphans(ctx, leased)
		total += n
		errs = append(errs, err)
	}
	if r.GitHub != nil {
		n, err := r.GitHub.SweepOrphans(ctx, leased)
		total += n
		errs = append(errs, err)
	}
	return total, errors.Join(errs...)
}

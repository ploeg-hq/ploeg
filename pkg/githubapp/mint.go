package githubapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WriterPermissions are what a writing Run's installation token may do: push
// to the repository, open and update its pull request, and read checks and
// statuses (ADR-0086). "workflows" is absent, so a Run cannot change the
// repository's GitHub Actions workflows.
func WriterPermissions() Permissions {
	return Permissions{"contents": "write", "pull_requests": "write", "checks": "read", "statuses": "read", "metadata": "read"}
}

// ReaderPermissions are what a reading Run needs and nothing it could change
// the repository with: read its contents, pull requests, checks and commit
// statuses (ADR-0192).
func ReaderPermissions() Permissions {
	return Permissions{"contents": "read", "pull_requests": "read", "checks": "read", "statuses": "read", "metadata": "read"}
}

// ForgePermissions are what ploegd's own forge reads and comments need: read
// the repository, comment on and read pull requests, and read checks and
// statuses. ploegd never pushes.
func ForgePermissions() Permissions {
	return Permissions{"contents": "read", "pull_requests": "write", "checks": "read", "statuses": "read", "metadata": "read"}
}

// Token is one installation token and when GitHub expires it.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Mint mints a fresh installation token for owner/repo with perms, bypassing
// the cache Token keeps, so the caller owns it alone and can revoke it
// without affecting anyone else. A token GitHub grants beyond perms is
// revoked and refused. A Minter pinned to one Installation mints only
// through MintRepository: GitHub reads a bare repository name against the
// installation's own account, which may hold a same-named repository.
func (m *Minter) Mint(ctx context.Context, owner, repo string, perms Permissions) (Token, error) {
	if m.Installation != 0 {
		return Token{}, errors.New("githubapp: a minter pinned to one installation mints by repository id")
	}
	if err := m.mintable(owner, repo, perms); err != nil {
		return Token{}, err
	}
	installation, err := m.installation(ctx, owner, repo)
	if err != nil {
		return Token{}, err
	}
	minted, err := m.accessToken(ctx, installation, map[string]any{"repositories": []string{repo}, "permissions": perms})
	if err != nil {
		if errors.Is(err, errNotFound) {
			m.forgetInstallation(strings.ToLower(owner + "/" + repo))
		}
		return Token{}, err
	}
	return m.accepted(ctx, minted, owner, repo, perms)
}

// MintRepository mints a token for the repository GitHub knows by
// repositoryID through the pinned Installation, and refuses it unless that
// id is owner/repo: a token for another repository, or one outside the
// installation's grant, is ErrNotGranted (ADR-0193).
func (m *Minter) MintRepository(ctx context.Context, owner, repo string, repositoryID int64, perms Permissions) (Token, error) {
	if m.Installation == 0 || repositoryID == 0 {
		return Token{}, errors.New("githubapp: minting by repository id needs a pinned installation and the id")
	}
	if err := m.mintable(owner, repo, perms); err != nil {
		return Token{}, err
	}
	minted, err := m.accessToken(ctx, m.Installation, map[string]any{"repository_ids": []int64{repositoryID}, "permissions": perms})
	if errors.Is(err, errNotFound) || errors.Is(err, errUnprocessable) {
		return Token{}, fmt.Errorf("%w: %s/%s", ErrNotGranted, owner, repo)
	}
	if err != nil {
		return Token{}, err
	}
	if len(minted.Repositories) != 1 || minted.Repositories[0].ID != repositoryID ||
		!strings.EqualFold(minted.Repositories[0].FullName, owner+"/"+repo) {
		_ = m.Revoke(context.WithoutCancel(ctx), minted.Token)
		return Token{}, fmt.Errorf("%w: the token GitHub minted is not for %s/%s alone", ErrNotGranted, owner, repo)
	}
	return m.accepted(ctx, minted, owner, repo, perms)
}

type mintedToken struct {
	Token        string            `json:"token"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Permissions  map[string]string `json:"permissions"`
	Repositories []struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

func (m *Minter) mintable(owner, repo string, perms Permissions) error {
	if m.AppID == "" || m.Key == nil {
		return errors.New("githubapp: App id and private key are required")
	}
	if owner == "" || repo == "" || len(perms) == 0 {
		return errors.New("githubapp: a minted token needs a repository and its permissions")
	}
	return nil
}

func (m *Minter) accessToken(ctx context.Context, installation int64, body map[string]any) (mintedToken, error) {
	var minted mintedToken
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installation)
	err := m.appCall(ctx, http.MethodPost, path, body, &minted)
	return minted, err
}

func (m *Minter) accepted(ctx context.Context, minted mintedToken, owner, repo string, perms Permissions) (Token, error) {
	if minted.Token == "" {
		return Token{}, errors.New("githubapp: GitHub returned no installation token")
	}
	for name, level := range minted.Permissions {
		if want, asked := perms[name]; !asked || (want == "read" && level != "read") {
			_ = m.Revoke(context.WithoutCancel(ctx), minted.Token)
			return Token{}, fmt.Errorf("githubapp: GitHub granted %s:%s beyond what was asked for %s/%s", name, level, owner, repo)
		}
	}
	return Token{Value: minted.Token, ExpiresAt: minted.ExpiresAt}, nil
}

// Revoke ends an installation token before it expires. A token GitHub no
// longer accepts (401) has ended already and counts as revoked.
func (m *Minter) Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, m.apiURL()+"/installation/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	}
	return fmt.Errorf("githubapp: revoke installation token: HTTP %d", resp.StatusCode)
}

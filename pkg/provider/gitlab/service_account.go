package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// maintainerAccess is GitLab's Maintainer role: the least that may create a
// project access token and manage a project's webhooks.
const maintainerAccess = 40

// ServiceAccountScopes are the scopes of a service account's token: api to
// mint project access tokens and manage webhooks, self_rotate to rotate
// itself without an Owner.
var ServiceAccountScopes = []string{"api", "self_rotate"}

// ServiceAccountTokenName is the name of the token Ploeg creates for a
// service account, as GitLab's UI shows it.
const ServiceAccountTokenName = "ploeg-connection"

type gitlabGroup struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
}

type gitlabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type gitlabToken struct {
	ID        int64    `json:"id"`
	Token     string   `json:"token"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	UserID    int64    `json:"user_id"`
	Active    bool     `json:"active"`
	Revoked   bool     `json:"revoked"`
}

func refused(step, reason string) error {
	return fmt.Errorf("%w: %s: %s", provider.ErrServiceAccountRefused, step, reason)
}

func (Connector) group(ctx context.Context, api, token, path string) (gitlabGroup, error) {
	var g gitlabGroup
	err := rest(ctx, http.MethodGet, api+"/groups/"+url.PathEscape(path)+"?with_projects=false", token, nil, &g)
	switch statusOf(err) {
	case 0:
		return g, err
	case http.StatusNotFound, http.StatusForbidden:
		return g, refused("read group "+path, "the group does not exist or the token cannot see it")
	}
	return g, err
}

// BootstrapServiceAccount makes a group service account Ploeg acts as
// (ADR-0203): it reuses or creates the account in the top-level group of
// req.Group, makes it a Maintainer of req.Group, and creates its token with
// ServiceAccountScopes. A failure part-way undoes what this call made.
func (c Connector) BootstrapServiceAccount(ctx context.Context, inst provider.ConnectionInstance, req provider.ServiceAccountRequest) (provider.ServiceAccountGrant, error) {
	api := apiRoot(inst)
	grant := provider.ServiceAccountGrant{}
	group, err := c.group(ctx, api, req.OwnerToken, strings.Trim(req.Group, "/"))
	if err != nil {
		return grant, err
	}
	top, _, _ := strings.Cut(group.FullPath, "/")
	topGroup := group
	if top != group.FullPath {
		if topGroup, err = c.group(ctx, api, req.OwnerToken, top); err != nil {
			return grant, err
		}
	}
	grant.Group, grant.GroupID = group.FullPath, strconv.FormatInt(group.ID, 10)
	grant.TopGroup, grant.TopGroupID = topGroup.FullPath, strconv.FormatInt(topGroup.ID, 10)
	accounts := api + "/groups/" + grant.TopGroupID + "/service_accounts"
	user, found, err := findServiceAccount(ctx, accounts, req.OwnerToken, req.Username)
	if err != nil {
		return grant, err
	}
	if !found {
		err := rest(ctx, http.MethodPost, accounts, req.OwnerToken, map[string]string{"name": req.Name, "username": req.Username}, &user)
		switch statusOf(err) {
		case 0:
			if err != nil {
				return grant, err
			}
		case http.StatusForbidden:
			return grant, refused("create service account", "the token's person is not an Owner of "+topGroup.FullPath+", or the group's plan has no service accounts")
		default:
			return grant, refused("create service account", err.Error())
		}
		grant.CreatedUser = true
	}
	grant.Account = provider.ConnectionAccount{ID: strconv.FormatInt(user.ID, 10), Login: user.Username}
	undo := func(cause error) (provider.ServiceAccountGrant, error) {
		if uerr := c.UndoServiceAccount(context.WithoutCancel(ctx), inst, req.OwnerToken, grant); uerr != nil {
			return provider.ServiceAccountGrant{}, errors.Join(cause, fmt.Errorf("gitlab: undo the service account bootstrap: %w", uerr))
		}
		return provider.ServiceAccountGrant{}, cause
	}
	if err := c.ensureMaintainer(ctx, api, req.OwnerToken, &grant); err != nil {
		return undo(err)
	}
	tok, err := createServiceAccountToken(ctx, accounts+"/"+grant.Account.ID+"/personal_access_tokens", req.OwnerToken, req.ExpiresAt)
	if err != nil {
		return undo(err)
	}
	grant.Token, grant.TokenID = tok.Token, strconv.FormatInt(tok.ID, 10)
	grant.ExpiresAt = parseDate(tok.ExpiresAt, req.ExpiresAt)
	return grant, nil
}

func findServiceAccount(ctx context.Context, accounts, token, username string) (gitlabUser, bool, error) {
	for page := 1; page <= 50; page++ {
		var batch []gitlabUser
		err := rest(ctx, http.MethodGet, accounts+"?per_page=100&order_by=username&sort=asc&page="+strconv.Itoa(page), token, nil, &batch)
		switch statusOf(err) {
		case 0:
			if err != nil {
				return gitlabUser{}, false, err
			}
		case http.StatusForbidden, http.StatusNotFound:
			return gitlabUser{}, false, refused("list service accounts", "the token's person is not an Owner of the top-level group, or the group's plan has no service accounts")
		default:
			return gitlabUser{}, false, err
		}
		for _, u := range batch {
			if strings.EqualFold(u.Username, username) {
				return u, true, nil
			}
		}
		if len(batch) < 100 {
			return gitlabUser{}, false, nil
		}
	}
	return gitlabUser{}, false, nil
}

func (Connector) ensureMaintainer(ctx context.Context, api, token string, grant *provider.ServiceAccountGrant) error {
	members := api + "/groups/" + grant.GroupID + "/members"
	err := rest(ctx, http.MethodPost, members, token, map[string]any{"user_id": grant.Account.ID, "access_level": maintainerAccess}, nil)
	switch statusOf(err) {
	case 0:
		if err != nil {
			return err
		}
		grant.AddedMember = true
		return nil
	case http.StatusConflict:
	default:
		return refused("add the service account to "+grant.Group, err.Error())
	}
	var member struct {
		AccessLevel int `json:"access_level"`
	}
	if err := rest(ctx, http.MethodGet, members+"/"+grant.Account.ID, token, nil, &member); err != nil {
		return err
	}
	if member.AccessLevel >= maintainerAccess {
		return nil
	}
	if err := rest(ctx, http.MethodPut, members+"/"+grant.Account.ID, token, map[string]any{"access_level": maintainerAccess}, nil); err != nil {
		return refused("raise the service account to Maintainer on "+grant.Group, err.Error())
	}
	grant.AddedMember, grant.PreviousLevel = true, member.AccessLevel
	return nil
}

func createServiceAccountToken(ctx context.Context, tokens, ownerToken string, expires time.Time) (gitlabToken, error) {
	scopes := ServiceAccountScopes
	for {
		var tok gitlabToken
		body := map[string]any{"name": ServiceAccountTokenName, "scopes": scopes, "expires_at": expires.UTC().Format(time.DateOnly)}
		err := rest(ctx, http.MethodPost, tokens, ownerToken, body, &tok)
		if statusOf(err) == http.StatusBadRequest && len(scopes) > 1 && strings.Contains(err.Error(), "scopes") {
			scopes = []string{"api"}
			continue
		}
		if statusOf(err) != 0 {
			return tok, refused("create the service account's token", err.Error())
		}
		if err != nil {
			return tok, err
		}
		if tok.Token == "" {
			return tok, refused("create the service account's token", "GitLab answered no token")
		}
		return tok, nil
	}
}

func parseDate(raw string, fallback time.Time) time.Time {
	if at, err := time.Parse(time.DateOnly, raw); err == nil {
		return at.UTC()
	}
	return fallback.UTC()
}

// UndoServiceAccount removes what a bootstrap made: the token it created,
// the account when it created it, and otherwise the membership it added or
// raised. A step GitLab reports gone is done.
func (Connector) UndoServiceAccount(ctx context.Context, inst provider.ConnectionInstance, ownerToken string, grant provider.ServiceAccountGrant) error {
	api := apiRoot(inst)
	if grant.Account.ID == "" {
		return nil
	}
	account := api + "/groups/" + grant.TopGroupID + "/service_accounts/" + grant.Account.ID
	var errs []error
	gone := func(err error) error {
		if statusOf(err) == http.StatusNotFound {
			return nil
		}
		return err
	}
	if grant.TokenID != "" {
		errs = append(errs, gone(rest(ctx, http.MethodDelete, account+"/personal_access_tokens/"+grant.TokenID, ownerToken, nil, nil)))
	}
	switch {
	case grant.CreatedUser:
		errs = append(errs, gone(rest(ctx, http.MethodDelete, account+"?hard_delete=true", ownerToken, nil, nil)))
	case grant.AddedMember && grant.PreviousLevel == 0:
		errs = append(errs, gone(rest(ctx, http.MethodDelete, api+"/groups/"+grant.GroupID+"/members/"+grant.Account.ID, ownerToken, nil, nil)))
	case grant.AddedMember:
		errs = append(errs, gone(rest(ctx, http.MethodPut, api+"/groups/"+grant.GroupID+"/members/"+grant.Account.ID, ownerToken,
			map[string]any{"access_level": grant.PreviousLevel}, nil)))
	}
	return errors.Join(errs...)
}

// RevokeOwnToken revokes token with itself through DELETE
// /personal_access_tokens/self. A token GitLab no longer accepts is dead
// already, which is the end this call wants.
func (Connector) RevokeOwnToken(ctx context.Context, inst provider.ConnectionInstance, token string) error {
	err := rest(ctx, http.MethodDelete, apiRoot(inst)+"/personal_access_tokens/self", token, nil, nil)
	if statusOf(err) == http.StatusUnauthorized {
		return nil
	}
	return err
}

// RotateOwnToken rotates token with itself through POST
// /personal_access_tokens/self/rotate. GitLab revokes token at once.
func (Connector) RotateOwnToken(ctx context.Context, inst provider.ConnectionInstance, token string, expiresAt time.Time) (provider.RotatedToken, error) {
	var tok gitlabToken
	err := rest(ctx, http.MethodPost, apiRoot(inst)+"/personal_access_tokens/self/rotate", token,
		map[string]string{"expires_at": expiresAt.UTC().Format(time.DateOnly)}, &tok)
	if err != nil {
		return provider.RotatedToken{}, err
	}
	if tok.Token == "" {
		return provider.RotatedToken{}, fmt.Errorf("gitlab: rotation answered no token: %w", provider.ErrVendorUnavailable)
	}
	return provider.RotatedToken{Token: tok.Token, ID: strconv.FormatInt(tok.ID, 10), ExpiresAt: parseDate(tok.ExpiresAt, expiresAt)}, nil
}

// tokenSelf reads the personal access token token is, with its scopes.
func tokenSelf(ctx context.Context, inst provider.ConnectionInstance, token string) (gitlabToken, error) {
	var tok gitlabToken
	err := rest(ctx, http.MethodGet, apiRoot(inst)+"/personal_access_tokens/self", token, nil, &tok)
	if s := statusOf(err); s == http.StatusForbidden || s == http.StatusNotFound || s == http.StatusBadRequest {
		return tok, fmt.Errorf("gitlab: the token is not a personal access token: %w", provider.ErrCredentialRefused)
	}
	return tok, err
}

// maintainerReach reports whether token's user is a Maintainer or above of
// at least one project, so it can create project access tokens.
func maintainerReach(ctx context.Context, inst provider.ConnectionInstance, token string) (bool, error) {
	var projects []struct {
		ID int64 `json:"id"`
	}
	if err := rest(ctx, http.MethodGet, apiRoot(inst)+"/projects?min_access_level=40&simple=true&per_page=1", token, nil, &projects); err != nil {
		return false, err
	}
	return len(projects) > 0, nil
}

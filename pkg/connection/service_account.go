package connection

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// rotationMargin is how long before its expiry the hourly pass rotates a
// service account's token (ADR-0203).
const rotationMargin = 30 * 24 * time.Hour

// newSigningToken is a webhook secret in GitLab's signing token format,
// whsec_ and the base64 of 32 random bytes, which also serves as a plain
// X-Gitlab-Token.
func newSigningToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

// serviceAccountUsername derives the service account's username from the
// connection id or label the flow names, else from the group, so a second
// flow for the same connection reuses the account.
func serviceAccountUsername(start StartRequest, group string) string {
	base := start.ID
	if base == "" && start.Connection != "" {
		base = start.Connection
	}
	if base == "" {
		base = start.Label
	}
	if base == "" {
		base = group
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 48 {
		name = strings.Trim(name[:48], "-")
	}
	if name == "" {
		name = "connection"
	}
	return "ploeg-" + name
}

// completeServiceAccount turns a group Owner's short-lived token into a
// GitLab group service account with its own token (ADR-0203). The Owner's
// token is used only for the bootstrap and revoked with itself at the end,
// whether the connection was made or not; a failure to revoke it is a
// warning in the answer and an audit row, never silent.
func (s *Service) completeServiceAccount(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, ownerToken, group string) (Completed, *Error) {
	inst := s.Instances[start.Instance]
	conn := s.Connectors[start.Kind]
	if conn.ServiceAccounts == nil {
		return Completed{}, refuse(422, "capability_missing", "This kind cannot bootstrap a service account (ServiceAccountBootstrapper).")
	}
	expires := s.now().UTC().AddDate(0, 0, s.Config.serviceAccountDays(start.Instance))
	username := serviceAccountUsername(start, group)
	name := start.Label
	if name == "" {
		name = "Ploeg " + username
	}
	out, e := func() (Completed, *Error) {
		grant, err := conn.ServiceAccounts.BootstrapServiceAccount(ctx, inst, provider.ServiceAccountRequest{OwnerToken: ownerToken, Group: group,
			Username: username, Name: name, ExpiresAt: expires})
		switch {
		case errors.Is(err, provider.ErrVendorUnavailable):
			return Completed{}, refuse(503, "vendor_unavailable", "GitLab could not be reached to make the service account; whatever was made was undone.")
		case errors.Is(err, provider.ErrCredentialRefused):
			return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "GitLab refused the Owner's token.",
				Checks: []Check{{Name: "owner_token_accepted", Result: "fail"}}}
		case errors.Is(err, provider.ErrServiceAccountRefused):
			return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "GitLab refused a step of the service account bootstrap; whatever was made was undone.",
				Checks: []Check{{Name: "service_account", Result: "fail", Detail: err.Error()}}}
		case err != nil:
			return Completed{}, refuse(503, "vendor_unavailable", "The service account bootstrap failed; whatever was made was undone.")
		}
		undo := func() {
			if err := conn.ServiceAccounts.UndoServiceAccount(context.WithoutCancel(ctx), inst, ownerToken, grant); err != nil {
				s.log().Error("service account bootstrap not undone at GitLab", "instance", inst.ID, "account", grant.Account.Login, "err", err)
			}
		}
		at := grant.ExpiresAt.UTC()
		account := grant.Account
		c := credential{values: map[string]string{"access_token": grant.Token, "token_id": grant.TokenID}, token: grant.Token,
			expires: &at, undo: undo, account: &account}
		done, e := s.finishCredential(ctx, caller, flow, start, c)
		if e != nil {
			return Completed{}, e
		}
		done.ServiceAccount = &ServiceAccount{Username: grant.Account.Login, Group: grant.Group, TopGroup: grant.TopGroup, Created: grant.CreatedUser, TokenExpiresAt: at}
		return done, nil
	}()
	if err := conn.ServiceAccounts.RevokeOwnToken(context.WithoutCancel(ctx), inst, ownerToken); err != nil {
		detail := map[string]any{"flow": flow.ID, "kind": start.Kind, "instance": start.Instance, "method": flow.Method, "group": group}
		if out.Connection != nil {
			detail["connection"] = out.Connection.ID
		}
		if aerr := s.Store.AuditConnection(ctx, s.audit(caller.Person, "connection.owner_token_not_revoked", &caller, detail)); aerr != nil {
			s.log().Error("owner token revocation failure not audited", "flow", flow.ID, "err", aerr)
		}
		s.log().Warn("the Owner's token could not be revoked; revoke it at GitLab", "flow", flow.ID, "err", err)
		if e == nil {
			out.Warnings = append(out.Warnings, Warning{Code: "owner_token_not_revoked",
				Message: "Ploeg could not revoke the token it was given; revoke it at GitLab under the person's access tokens."})
		}
	}
	return out, e
}

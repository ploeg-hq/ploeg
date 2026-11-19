package connection

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

var errRefreshRefused = errors.New("connection: the vendor refused the refresh")

func refreshMargin(lifetime int64) time.Duration {
	margin := time.Duration(lifetime) * time.Second / 10
	if margin < 5*time.Minute {
		margin = 5 * time.Minute
	}
	return margin
}

func (s *Service) due(row store.ConnectionRow) bool {
	if row.Method == MethodGitLabServiceAccount && row.ExpiresAt != nil {
		return !s.now().Before(row.ExpiresAt.Add(-rotationMargin))
	}
	if row.Method != MethodOAuth || row.ExpiresAt == nil {
		return false
	}
	var field secretsField
	_ = json.Unmarshal(row.Secrets, &field)
	return !s.now().Before(row.ExpiresAt.Add(-refreshMargin(field.Lifetime)))
}

// credentialFor returns the row's credential, refreshing an expiring OAuth
// token first.
func (s *Service) credentialFor(ctx context.Context, row store.ConnectionRow) (provider.ConnectionCredential, map[string]string, error) {
	if s.due(row) {
		if err := s.refresh(ctx, row.ID, false); err != nil {
			return provider.ConnectionCredential{}, nil, err
		}
		fresh, err := s.Store.Connection(ctx, s.tenant(), row.ID)
		if err != nil {
			return provider.ConnectionCredential{}, nil, err
		}
		row = fresh
	}
	return s.storedCredential(ctx, row)
}

// Credential returns a live connection's credential for a Run, a write or
// a read, refreshing an expiring token first (ADR-0195 section 5).
func (s *Service) Credential(ctx context.Context, id string) (provider.ConnectionCredential, error) {
	row, err := s.Store.Connection(ctx, s.tenant(), id)
	if err != nil || row.RemovedAt != nil {
		return provider.ConnectionCredential{}, store.ErrConnectionNotFound
	}
	cred, _, err := s.credentialFor(ctx, row)
	return cred, err
}

// CredentialRefused handles a vendor answering 401 to a token Ploeg
// believed valid: an expiring OAuth token is refreshed once, anything else
// makes the connection invalid with credential_refused.
func (s *Service) CredentialRefused(ctx context.Context, id string) error {
	row, err := s.Store.Connection(ctx, s.tenant(), id)
	if err != nil || row.RemovedAt != nil {
		return store.ErrConnectionNotFound
	}
	if row.Method == MethodOAuth && row.ExpiresAt != nil {
		return s.refresh(ctx, id, true)
	}
	report := ProbeReport{At: s.now().UTC(), Result: "fail", State: StateInvalid, Reason: "credential_refused",
		Checks: []Check{{Name: "credential_accepted", Result: "fail", Detail: "the vendor refused the credential"}}}
	return s.applyProbe(ctx, row, report, "connection:"+row.ID, nil)
}

// refresh exchanges the stored refresh token under the row's lock, so a
// rotating refresh token is never spent twice, and writes the new tokens as
// one new version under the same reference. A service account's token is
// rotated with itself instead (ADR-0203).
func (s *Service) refresh(ctx context.Context, id string, force bool) error {
	var refused *store.ConnectionRow
	err := s.Store.RefreshConnection(ctx, s.tenant(), id, func(row store.ConnectionRow) (json.RawMessage, *time.Time, bool, error) {
		if !force && !s.due(row) {
			return nil, nil, false, nil
		}
		var field secretsField
		if err := json.Unmarshal(row.Secrets, &field); err != nil || field.Credential == nil {
			return nil, nil, false, errRefreshRefused
		}
		values, err := s.Secrets.Get(ctx, *field.Credential)
		if err != nil {
			return nil, nil, false, err
		}
		conn := s.Connectors[row.Kind]
		if row.Method == MethodGitLabServiceAccount {
			return s.rotate(ctx, row, field, values, &refused)
		}
		if conn.Authorizer == nil || values["refresh_token"] == "" {
			refused = &row
			return nil, nil, false, errRefreshRefused
		}
		cred, err := conn.Authorizer.Refresh(ctx, s.Instances[row.Instance], field.RedirectURI, values["refresh_token"])
		if errors.Is(err, provider.ErrCredentialRefused) {
			refused = &row
			return nil, nil, false, errRefreshRefused
		}
		if err != nil {
			return nil, nil, false, err
		}
		next := maps.Clone(values)
		next["access_token"] = cred.AccessToken
		if cred.RefreshToken != "" {
			next["refresh_token"] = cred.RefreshToken
		}
		ref, err := s.Secrets.Put(ctx, field.Credential.Path, next)
		if err != nil {
			return nil, nil, false, err
		}
		field.Credential = &ref
		var expires *time.Time
		if !cred.ExpiresAt.IsZero() {
			at := cred.ExpiresAt.UTC()
			expires = &at
			field.Lifetime = int64(cred.ExpiresAt.Sub(s.now()).Seconds())
		}
		raw, _ := json.Marshal(field)
		return raw, expires, true, nil
	})
	if refused != nil {
		report := ProbeReport{At: s.now().UTC(), Result: "fail", State: StateInvalid, Reason: "refresh_failed",
			Checks: []Check{{Name: "credential_accepted", Result: "fail", Detail: "the vendor refused the refresh"}}}
		if err := s.applyProbe(ctx, *refused, report, "connection:"+refused.ID, nil); err != nil {
			s.log().Error("refresh failure not recorded", "connection", refused.ID, "err", err)
		}
	}
	return err
}

// rotationWrites is how often a rotated token's write to the store is
// tried: GitLab revoked the old token when it answered the new one, so a
// lost write loses the connection.
const rotationWrites = 3

func (s *Service) rotate(ctx context.Context, row store.ConnectionRow, field secretsField, values map[string]string, refused **store.ConnectionRow) (json.RawMessage, *time.Time, bool, error) {
	conn := s.Connectors[row.Kind]
	if conn.Rotator == nil || values["access_token"] == "" {
		return nil, nil, false, nil
	}
	expires := s.now().UTC().AddDate(0, 0, s.Config.serviceAccountDays(row.Instance))
	rotated, err := conn.Rotator.RotateOwnToken(ctx, s.Instances[row.Instance], values["access_token"], expires)
	if errors.Is(err, provider.ErrCredentialRefused) {
		*refused = &row
		return nil, nil, false, errRefreshRefused
	}
	if err != nil {
		return nil, nil, false, err
	}
	next := maps.Clone(values)
	next["access_token"], next["token_id"] = rotated.Token, rotated.ID
	var ref secretstore.Ref
	for try := 0; try < rotationWrites; try++ {
		if ref, err = s.Secrets.Put(context.WithoutCancel(ctx), field.Credential.Path, next); err == nil {
			break
		}
	}
	if err != nil {
		s.log().Error("a rotated service account token was not stored; GitLab revoked the old one, so the connection must be reconnected", "connection", row.ID, "err", err)
		*refused = &row
		return nil, nil, false, errRefreshRefused
	}
	field.Credential = &ref
	at := rotated.ExpiresAt.UTC()
	raw, _ := json.Marshal(field)
	return raw, &at, true, nil
}

package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// probeCredential runs the vendor checks of ADR-0195 section 8 against
// cred. expected, when set, is the account the connection must still act
// as. The report's State and Reason say what the connection becomes;
// Unknown means a vendor could not be reached and the state stays.
func (s *Service) probeCredential(ctx context.Context, port, kind string, inst provider.ConnectionInstance, cred provider.ConnectionCredential, expected *store.ConnectionRow) ProbeReport {
	report := ProbeReport{At: s.now().UTC(), Result: "pass", State: StateActive, Checks: []Check{}}
	fail := func(name, reason, detail string) {
		report.Checks = append(report.Checks, Check{Name: name, Result: "fail", Detail: detail})
		if report.Result == "pass" {
			report.Result, report.State, report.Reason = "fail", StateInvalid, reason
		}
	}
	conn := s.Connectors[kind]
	if conn.Prober == nil {
		report.Unknown, report.Result = true, "unknown"
		report.Checks = append(report.Checks, Check{Name: "credential_accepted", Result: "unknown", Detail: "the adapter declares no ConnectionProber"})
		return report
	}
	probe, err := conn.Prober.ProbeConnection(ctx, inst, cred)
	switch {
	case errors.Is(err, provider.ErrInstallationSuspended):
		fail("credential_accepted", "installation_suspended", "the App installation is suspended")
		return report
	case errors.Is(err, provider.ErrForeignInstallation):
		fail("credential_accepted", "installation_deleted", "the installation belongs to another App")
		return report
	case errors.Is(err, provider.ErrCredentialRefused):
		reason := "credential_refused"
		if cred.InstallationID != "" {
			reason = "installation_deleted"
		}
		fail("credential_accepted", reason, "the vendor refused the credential")
		return report
	case err != nil:
		report.Unknown, report.Result = true, "unknown"
		report.Checks = append(report.Checks, Check{Name: "credential_accepted", Result: "unknown", Detail: "the vendor could not be reached"})
		return report
	}
	report.Checks = append(report.Checks, Check{Name: "credential_accepted", Result: "pass"})
	account := probe.Account
	report.Account, report.Accounts = &account, account
	if expected != nil && !strings.HasPrefix(expected.AccountID, "installation:") {
		if expected.AccountID != account.ID {
			fail("account_matches", "account_changed", "the credential now acts as another account")
		} else {
			report.Checks = append(report.Checks, Check{Name: "account_matches", Result: "pass"})
		}
	}
	report.Scopes, report.Missing = probe.Scopes, probe.Missing
	if len(probe.Missing) > 0 {
		fail("scopes", "scopes_insufficient", "missing "+strings.Join(probe.Missing, ", "))
	} else {
		report.Checks = append(report.Checks, Check{Name: "scopes", Result: "pass"})
	}
	if port == PortForge && probe.CanMint != nil {
		report.CanMint = probe.CanMint
		result := "pass"
		if !*probe.CanMint {
			result = "fail"
		}
		report.Checks = append(report.Checks, Check{Name: "can_mint", Result: result, Detail: "informational: whether per-Run credentials can be minted"})
	}
	if port == PortTracker && probe.Visible != nil {
		report.Visible = probe.Visible
		report.Checks = append(report.Checks, Check{Name: "scopes_visible", Result: "pass", Detail: fmt.Sprintf("%d", probe.Visible.Count)})
	}
	return report
}

// storedCredential reads the credential a row references. A missing value
// is token_missing; an unreachable store is unknown.
func (s *Service) storedCredential(ctx context.Context, row store.ConnectionRow) (provider.ConnectionCredential, map[string]string, error) {
	var field secretsField
	if err := json.Unmarshal(row.Secrets, &field); err != nil {
		return provider.ConnectionCredential{}, nil, secretstore.ErrNotFound
	}
	cred := provider.ConnectionCredential{Port: row.Port, InstallationID: row.InstallationID}
	if field.App != nil {
		app, err := s.Store.ConnectionApp(ctx, s.tenant(), row.App)
		if err != nil {
			return cred, nil, secretstore.ErrNotFound
		}
		values, err := s.Secrets.Get(ctx, *field.App)
		if err != nil {
			return cred, nil, err
		}
		cred.AppID, cred.PrivateKey = app.AppID, values["private_key"]
		return cred, values, nil
	}
	if field.Credential == nil {
		return cred, nil, secretstore.ErrNotFound
	}
	values, err := s.Secrets.Get(ctx, *field.Credential)
	if err != nil {
		return cred, nil, err
	}
	cred.Token = values["access_token"]
	return cred, values, nil
}

// probeRow probes a stored connection, store check first.
func (s *Service) probeRow(ctx context.Context, row store.ConnectionRow) ProbeReport {
	if row.Method == MethodGitHubAppInstallation && row.InstallationID == "" {
		return ProbeReport{At: s.now().UTC(), Result: "unknown", Unknown: true,
			Checks: []Check{{Name: "installation_approved", Result: "unknown", Detail: "the installation awaits an organisation owner's approval"}}}
	}
	cred, _, err := s.credentialFor(ctx, row)
	if err != nil {
		report := ProbeReport{At: s.now().UTC(), Checks: []Check{}}
		switch {
		case errors.Is(err, secretstore.ErrNotFound):
			report.Result, report.State = "fail", StateTokenMissing
			report.Checks = append(report.Checks, Check{Name: "secret_store", Result: "fail", Detail: "nothing is stored at the connection's reference"})
		case errors.Is(err, errRefreshRefused):
			report.Result, report.State, report.Reason = "fail", StateInvalid, "refresh_failed"
			report.Checks = append(report.Checks, Check{Name: "secret_store", Result: "pass"}, Check{Name: "credential_accepted", Result: "fail", Detail: "the vendor refused the refresh"})
		default:
			report.Result, report.Unknown = "unknown", true
			report.Checks = append(report.Checks, Check{Name: "secret_store", Result: "unknown", Detail: "the secret store or vendor could not be reached"})
		}
		return report
	}
	report := s.probeCredential(ctx, row.Port, row.Kind, s.Instances[row.Instance], cred, &row)
	report.Checks = append([]Check{{Name: "secret_store", Result: "pass"}}, report.Checks...)
	return report
}

// applyProbe records report on row, auditing a change of state.
func (s *Service) applyProbe(ctx context.Context, row store.ConnectionRow, report ProbeReport, actor string, caller *Caller) error {
	raw, _ := json.Marshal(report)
	state, reason := report.State, report.Reason
	if report.Unknown {
		state, reason = row.State, row.InvalidReason
	}
	if state == StateActive && row.State == StateAwaitingConsent && report.Account != nil && strings.HasPrefix(row.AccountID, "installation:") {
		if err := s.Store.SetConnectionAccount(ctx, row.Tenant, row.ID, report.Account.ID, mustJSON(report.Account)); err != nil {
			return err
		}
	}
	var audits []store.ConnectionAudit
	if state != row.State || reason != row.InvalidReason {
		detail := map[string]any{"connection": row.ID, "kind": row.Kind, "instance": row.Instance, "state": state, "previousState": row.State}
		action := "connection.restored"
		if state != StateActive {
			action = "connection.invalid"
			detail["invalidReason"] = reason
		}
		audits = append(audits, s.audit(actor, action, caller, detail))
	}
	return s.Store.SetConnectionState(ctx, row.Tenant, row.ID, state, reason, raw, audits)
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

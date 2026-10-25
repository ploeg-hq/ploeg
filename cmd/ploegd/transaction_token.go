package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/txntoken"
)

// transactionTokenSigner reads the key ploegd signs each Run's transaction
// token with (ADR-0156). No key signs none; an issuer, audience or retired
// keys without a key, or a key without an issuer, stops ploegd before it
// serves.
func transactionTokenSigner(log *slog.Logger) (*txntoken.Signer, error) {
	key := os.Getenv("PLOEG_TRANSACTION_TOKEN_SIGNING_KEY")
	issuer := os.Getenv("PLOEG_TRANSACTION_TOKEN_ISSUER")
	audience := os.Getenv("PLOEG_TRANSACTION_TOKEN_AUDIENCE")
	retired := os.Getenv("PLOEG_TRANSACTION_TOKEN_RETIRED_KEYS")
	if key == "" {
		if issuer != "" || audience != "" || retired != "" {
			return nil, errors.New("PLOEG_TRANSACTION_TOKEN_ISSUER, _AUDIENCE and _RETIRED_KEYS need PLOEG_TRANSACTION_TOKEN_SIGNING_KEY")
		}
		return nil, nil
	}
	s, err := txntoken.NewSigner(issuer, audience, key, retired)
	if err != nil {
		return nil, fmt.Errorf("PLOEG_TRANSACTION_TOKEN_SIGNING_KEY: %w", err)
	}
	log.Info("each Run gets a transaction token", "issuer", s.Issuer(), "trust_domain", s.Audience(), "kid", s.KeyID(),
		"keys", httpapi.TransactionTokenKeysPath)
	return s, nil
}

// workerSecurityFromEnv reads managed worker security. PLOEG_WORKER_HMAC_CAPABILITY
// is "issue" (the default, deprecated by ADR-0156) or "off"; off needs a
// transaction token signer and managed mode, and no worker signing key.
func workerSecurityFromEnv(signer *txntoken.Signer, log *slog.Logger) (*httpapi.WorkerSecurity, error) {
	legacy := os.Getenv("PLOEG_WORKER_AUTH_MODE") == "legacy"
	switch mode := os.Getenv("PLOEG_WORKER_HMAC_CAPABILITY"); mode {
	case "", "issue":
		a, err := httpapi.NewWorkerSecurity(os.Getenv("PLOEG_WORKER_BOOTSTRAPS"), os.Getenv("PLOEG_WORKER_SIGNING_KEY"), legacy)
		if err == nil && !legacy {
			log.Warn("workers still get the HMAC capability, which is deprecated (ADR-0156): set PLOEG_WORKER_HMAC_CAPABILITY=off once every worker presents its transaction token",
				"transaction_tokens", signer != nil)
		}
		return a, err
	case "off":
		if legacy {
			return nil, errors.New("PLOEG_WORKER_HMAC_CAPABILITY=off needs managed worker mode")
		}
		if signer == nil {
			return nil, errors.New("PLOEG_WORKER_HMAC_CAPABILITY=off needs PLOEG_TRANSACTION_TOKEN_SIGNING_KEY: workers would get no credential")
		}
		if os.Getenv("PLOEG_WORKER_SIGNING_KEY") != "" {
			return nil, errors.New("PLOEG_WORKER_SIGNING_KEY signs the HMAC capability, which PLOEG_WORKER_HMAC_CAPABILITY=off retires: remove it")
		}
		return httpapi.NewTransactionTokenWorkerSecurity(os.Getenv("PLOEG_WORKER_BOOTSTRAPS"))
	default:
		return nil, fmt.Errorf("PLOEG_WORKER_HMAC_CAPABILITY %q: want issue or off", mode)
	}
}

// warnUnsponsoredTeams logs each configured Team that will run nothing
// because it names no sponsor while ploegd signs transaction tokens.
func warnUnsponsoredTeams(signer *txntoken.Signer, teams []string, sponsors map[string]string, log *slog.Logger) {
	if signer == nil {
		return
	}
	sort.Strings(teams)
	for _, team := range teams {
		if sponsors[team] == "" {
			log.Warn("this Team has no sponsor and runs nothing (ADR-0156): set teams.<name>.sponsor", "team", team)
		}
	}
}

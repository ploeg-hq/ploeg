package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

// runIdentitySigner reads the Run identity key (ADR-0149). No key issues no
// Run identities; a key without an issuer, or an issuer without a key, stops
// ploegd before it serves, so a half-configured deployment is never silent.
func runIdentitySigner(log *slog.Logger) (*runidentity.Signer, error) {
	key := os.Getenv("PLOEG_RUN_IDENTITY_SIGNING_KEY")
	issuer := os.Getenv("PLOEG_RUN_IDENTITY_ISSUER")
	retired := os.Getenv("PLOEG_RUN_IDENTITY_RETIRED_KEYS")
	if key == "" {
		if issuer != "" || retired != "" {
			return nil, fmt.Errorf("PLOEG_RUN_IDENTITY_ISSUER and PLOEG_RUN_IDENTITY_RETIRED_KEYS need PLOEG_RUN_IDENTITY_SIGNING_KEY")
		}
		return nil, nil
	}
	s, err := runidentity.NewSigner(issuer, key, retired)
	if err != nil {
		return nil, fmt.Errorf("PLOEG_RUN_IDENTITY_SIGNING_KEY: %w", err)
	}
	log.Info("run identities are issued to plugins that opt in", "issuer", s.Issuer(), "kid", s.KeyID(),
		"keys", httpapi.RunIdentityKeysPath)
	return s, nil
}

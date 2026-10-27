package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

// runIdentitySigner reads the Run identity key and the audiences it signs for
// (ADR-0149). No key issues no Run identities; a key without an issuer, or an
// issuer, retired keys or audiences without a key, stops ploegd before it
// serves, so a half-configured deployment is never silent. A key without
// PLOEG_RUN_IDENTITY_AUDIENCES signs for no audience (threat model F-48).
func runIdentitySigner(log *slog.Logger) (*runidentity.Signer, runidentity.Audiences, error) {
	key := os.Getenv("PLOEG_RUN_IDENTITY_SIGNING_KEY")
	issuer := os.Getenv("PLOEG_RUN_IDENTITY_ISSUER")
	retired := os.Getenv("PLOEG_RUN_IDENTITY_RETIRED_KEYS")
	allowlist := os.Getenv("PLOEG_RUN_IDENTITY_AUDIENCES")
	if key == "" {
		if issuer != "" || retired != "" || allowlist != "" {
			return nil, nil, fmt.Errorf("PLOEG_RUN_IDENTITY_ISSUER, PLOEG_RUN_IDENTITY_RETIRED_KEYS and PLOEG_RUN_IDENTITY_AUDIENCES need PLOEG_RUN_IDENTITY_SIGNING_KEY")
		}
		return nil, nil, nil
	}
	s, err := runidentity.NewSigner(issuer, key, retired)
	if err != nil {
		return nil, nil, fmt.Errorf("PLOEG_RUN_IDENTITY_SIGNING_KEY: %w", err)
	}
	audiences, err := runidentity.ParseAudiences(allowlist)
	if err != nil {
		return nil, nil, fmt.Errorf("PLOEG_RUN_IDENTITY_AUDIENCES: %w", err)
	}
	if len(audiences) == 0 {
		log.Warn("run identities are signed for no audience: PLOEG_RUN_IDENTITY_AUDIENCES names none, so every request answers 403")
	}
	log.Info("run identities are issued to plugins that opt in", "issuer", s.Issuer(), "kid", s.KeyID(),
		"keys", httpapi.RunIdentityKeysPath, "teams", len(audiences))
	return s, audiences, nil
}

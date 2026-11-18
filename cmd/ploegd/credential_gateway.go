package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
)

func credentialGatewaysFromEnv(workerSecurity *httpapi.WorkerSecurity, log *slog.Logger) (*httpapi.CredentialGateways, error) {
	raw := os.Getenv("PLOEG_CREDENTIAL_GATEWAYS")
	if raw == "" {
		return nil, nil
	}
	if workerSecurity == nil || workerSecurity.AllowLegacy || workerSecurity.RetireHMAC {
		return nil, errors.New("PLOEG_CREDENTIAL_GATEWAYS needs managed worker mode with the HMAC capability: the gateway capability is the Run's control capability (ADR-0197)")
	}
	reviewer, err := kubeauth.InCluster()
	if err != nil {
		return nil, err
	}
	gateways, err := httpapi.NewCredentialGateways(raw, reviewer, []byte(os.Getenv("PLOEG_WORKER_SIGNING_KEY")))
	if err != nil {
		return nil, err
	}
	log.Info("Runs of gateway Teams get their credentials only through a credential gateway (ADR-0197)")
	return gateways, nil
}

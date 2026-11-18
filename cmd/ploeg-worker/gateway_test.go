package main

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/worker"
)

func TestAGatewayWorkerRefusesEveryCredentialInItsPod(t *testing.T) {
	t.Setenv("PLOEG_CREDENTIAL_GATEWAY_FORGE_URL", "http://gateway:8081")
	t.Setenv("LLM_BASE_URL", "http://gateway:8082/v1")
	if err := refuseCredentialsInGatewayMode(worker.Config{}); err != nil {
		t.Fatalf("a credential-free gateway pod was refused: %v", err)
	}
	for _, key := range append(gatewayModeForbiddenEnvironment, "PLOEG_CLAUDE_PLUGIN_ENV_TOKEN") {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "a-value")
			if err := refuseCredentialsInGatewayMode(worker.Config{}); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("%s in the pod: %v", key, err)
			}
		})
	}
	if err := refuseCredentialsInGatewayMode(worker.Config{ForgeTokenIsolation: worker.ForgeTokenIsolationProxy}); err == nil {
		t.Fatal("the in-worker forge proxy was accepted behind the gateway")
	}
	t.Setenv("PLOEG_CREDENTIAL_GATEWAY_FORGE_URL", "")
	if err := refuseCredentialsInGatewayMode(worker.Config{}); err == nil {
		t.Fatal("a gateway worker without the gateway's forge URL was accepted")
	}
}

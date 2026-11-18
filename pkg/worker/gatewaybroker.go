package worker

import (
	"context"
	"errors"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

type gatewayBroker struct{ api *APIClient }

func (b gatewayBroker) Mint(_ context.Context, req llmbroker.MintRequest) (llmbroker.Credential, error) {
	capability, ok := b.api.capabilities.Load(req.RunToken)
	if !ok || capability.(string) == "" {
		return llmbroker.Credential{}, errors.New("the Run has no gateway capability: its claim did not come through the credential gateway")
	}
	return llmbroker.Credential{APIKey: capability.(string), Alias: "gateway-" + prefix(req.RunToken, 12), RunToken: req.RunToken}, nil
}

func (b gatewayBroker) Revoke(ctx context.Context, cred llmbroker.Credential) error {
	return b.api.Revoke(ctx, cred)
}

func (b gatewayBroker) Spend(ctx context.Context, cred llmbroker.Credential) (float64, error) {
	return b.api.Spend(ctx, cred)
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

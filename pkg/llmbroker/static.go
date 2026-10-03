package llmbroker

import (
	"context"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
)

// Static is the no-gateway broker: Mint echoes a fixed key (empty = the
// harness image authenticates itself, e.g. baked credentials or a BYO-key
// gateway) and Revoke is a no-op. The alias keeps the same trace format so
// LLM_TRACE_ID / commit trailers stay joinable either way.
type Static struct {
	Key string
}

var _ Broker = Static{}

func (s Static) Mint(_ context.Context, req MintRequest) (Credential, error) {
	return Credential{APIKey: s.Key, Alias: litellm.Alias(req.RunToken)}, nil
}

func (s Static) Revoke(context.Context, Credential) error { return nil }

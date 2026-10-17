package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Attribution names who made an operator change and how (ADR-0108): the
// Operator Consumer, the person it names in X-Ploeg-Actor, the person acting
// on that person's behalf in X-Ploeg-Acting-User, the channel in X-Ploeg-Via
// and the confirmation in X-Ploeg-Confirmation. Every audit row an operator
// request writes carries it under the detail key "attribution".
type Attribution struct {
	Consumer     string `json:"consumer"`
	Actor        string `json:"actor"`
	ActingUser   string `json:"actingUser,omitempty"`
	Via          string `json:"via,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
}

// AuditAttributionKey is the audit_log detail key that holds an Attribution.
const AuditAttributionKey = "attribution"

type attributionKey struct{}

// WithAttribution returns ctx carrying a, so every audit row written with an
// operator: actor under ctx records it.
func WithAttribution(ctx context.Context, a Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, a)
}

// AttributionFrom returns the Attribution ctx carries.
func AttributionFrom(ctx context.Context) (Attribution, bool) {
	a, ok := ctx.Value(attributionKey{}).(Attribution)
	return a, ok && a.Consumer != "" && a.Actor != ""
}

func attributedDetail(ctx context.Context, actor string, detail []byte) ([]byte, error) {
	a, ok := AttributionFrom(ctx)
	if !ok || !strings.HasPrefix(actor, "operator:") {
		return detail, nil
	}
	fields := map[string]json.RawMessage{}
	if len(detail) > 0 && string(detail) != "null" {
		if err := json.Unmarshal(detail, &fields); err != nil {
			return detail, nil
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	fields[AuditAttributionKey] = raw
	return json.Marshal(fields)
}

package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Attribution names who made an operator change and how (ADR-0108): the
// Operator Consumer, the person it names in X-Ploeg-Actor, the person acting
// on that person's behalf in X-Ploeg-Acting-User, the channel in X-Ploeg-Via
// and the confirmation in X-Ploeg-Confirmation. Proof says how Ploeg knows
// the actor (ADR-0150): "proven" when a verified person token named them,
// "asserted" when only the consumer's header did. Act is the sub of the
// person token's RFC 8693 act claim, the party acting for the person. Every
// audit row an operator request writes carries it under the detail key
// "attribution".
type Attribution struct {
	Consumer     string `json:"consumer"`
	Actor        string `json:"actor"`
	ActingUser   string `json:"actingUser,omitempty"`
	Via          string `json:"via,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
	Proof        string `json:"proof,omitempty"`
	Act          string `json:"act,omitempty"`
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
	var key string
	var value any
	if a, ok := AttributionFrom(ctx); ok && strings.HasPrefix(actor, "operator:") {
		key, value = AuditAttributionKey, a
	} else if r, ok := RunActorFrom(ctx); ok {
		key, value = AuditRunActorKey, r
	} else {
		return detail, nil
	}
	fields := map[string]json.RawMessage{}
	if len(detail) > 0 && string(detail) != "null" {
		if err := json.Unmarshal(detail, &fields); err != nil {
			return detail, nil
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[key] = raw
	return json.Marshal(fields)
}

// AuditOperatorAttributionRefused is the audit action of an operator change
// refused because its attribution was not proven (ADR-0171).
const AuditOperatorAttributionRefused = "operator.attribution_refused"

// AttributionRefusal is an operator change ploegd refused for its
// attribution: the consumer that sent it, the stable reason code, the request
// line, and the person the consumer asserted, when it named one.
type AttributionRefusal struct {
	Consumer      string `json:"consumer"`
	Reason        string `json:"reason"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	AssertedActor string `json:"assertedActor,omitempty"`
}

// RecordAttributionRefusal writes r to the audit log under the consumer's
// operator: actor and without an attribution, since nothing was attributed.
func (s *Store) RecordAttributionRefusal(ctx context.Context, r AttributionRefusal) error {
	detail, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_log (actor, action, detail) VALUES ($1, $2, $3)`,
		"operator:"+r.Consumer, AuditOperatorAttributionRefused, detail)
	return err
}

package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
)

// ErrNoGatewayGrant reports that no credential gateway claimed the Run, so it
// has no grant to serve.
var ErrNoGatewayGrant = errors.New("store: the run has no credential gateway grant")

// GatewayGrant is a Run's sealed grant and the pin it is bound to.
type GatewayGrant struct {
	RunToken string
	Pin      gatewaycontract.Pin
	Sealed   []byte
}

// RecordGatewayGrant stores the sealed grant of a Run claimed through a
// credential gateway. It replaces the sealed credentials of an existing grant
// but never its pin.
func (s *Store) RecordGatewayGrant(ctx context.Context, g GatewayGrant) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO credential_gateway_grants (run_token, namespace, pod_name, pod_uid, pod_ip, sealed)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_token) DO UPDATE SET sealed = EXCLUDED.sealed, updated_at = now()`,
		g.RunToken, g.Pin.Namespace, g.Pin.PodName, g.Pin.PodUID, g.Pin.PodIP, g.Sealed)
	return err
}

// ResealGatewayGrant replaces the sealed credentials of a Run's grant.
func (s *Store) ResealGatewayGrant(ctx context.Context, runToken string, sealed []byte) error {
	tag, err := s.pool.Exec(ctx, `UPDATE credential_gateway_grants SET sealed = $2, updated_at = now() WHERE run_token = $1`, runToken, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNoGatewayGrant
	}
	return err
}

// GatewayGrantFor reads a Run's grant.
func (s *Store) GatewayGrantFor(ctx context.Context, runToken string) (GatewayGrant, error) {
	g := GatewayGrant{RunToken: runToken}
	err := s.pool.QueryRow(ctx, `SELECT namespace, pod_name, pod_uid, pod_ip, sealed FROM credential_gateway_grants WHERE run_token = $1`, runToken).
		Scan(&g.Pin.Namespace, &g.Pin.PodName, &g.Pin.PodUID, &g.Pin.PodIP, &g.Sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNoGatewayGrant
	}
	return g, err
}

// IsGatewayRun reports whether a credential gateway claimed the Run, so
// ploegd never hands its credentials to the pod.
func (s *Store) IsGatewayRun(ctx context.Context, runToken string) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credential_gateway_grants WHERE run_token = $1)`, runToken).Scan(&found)
	return found, err
}

// DropSettledGatewayGrants deletes the sealed grants of every Run that no
// longer runs, so a settled Run's credentials do not outlive it at rest.
func (s *Store) DropSettledGatewayGrants(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM credential_gateway_grants g USING agent_runs r
		WHERE r.run_token = g.run_token AND r.state <> 'running'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RecordGatewayAudit adds a gateway's per-Run totals, writes each refusal as
// a credential_gateway.refused audit row and each MCP call as an mcp.call
// audit row for the Run's Work Item.
func (s *Store) RecordGatewayAudit(ctx context.Context, namespace string, batch gatewaycontract.AuditBatch) error {
	uses, refusals := batch.Uses, batch.Refusals
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, u := range uses {
		if _, err := tx.Exec(ctx, `INSERT INTO credential_gateway_use (run_token, family, credential, requests, refused, bytes)
			SELECT g.run_token, $2, $3, $4, $5, $6 FROM credential_gateway_grants g WHERE g.run_token = $1 AND g.namespace = $7
			ON CONFLICT (run_token, family, credential) DO UPDATE SET
				requests = credential_gateway_use.requests + EXCLUDED.requests,
				refused = credential_gateway_use.refused + EXCLUDED.refused,
				bytes = credential_gateway_use.bytes + EXCLUDED.bytes,
				updated_at = now()`,
			u.RunToken, u.Family, u.Credential, u.Requests, u.Refused, u.Bytes, namespace); err != nil {
			return err
		}
	}
	for _, r := range refusals {
		detail, err := json.Marshal(map[string]any{"namespace": namespace, "family": r.Family, "method": r.Method,
			"route": r.Route, "reason": r.Reason, "status": r.Status})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, work_item_id, detail)
			SELECT 'credential-gateway:' || $2, 'credential_gateway.refused', r.work_item_id,
				$3::jsonb || jsonb_build_object('run', r.id)
			FROM agent_runs r JOIN credential_gateway_grants g USING (run_token)
			WHERE r.run_token = $1 AND g.namespace = $2`, r.RunToken, namespace, detail); err != nil {
			return err
		}
	}
	for _, c := range batch.MCPCalls {
		detail, err := json.Marshal(map[string]any{"namespace": namespace, "server_id": c.ServerID, "server": c.Server, "method": c.Method,
			"tool": c.Tool, "access": c.Access, "outcome": c.Outcome, "status": c.Status, "bytes_in": c.BytesIn, "bytes_out": c.BytesOut,
			"duration_ms": c.DurationMS})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, work_item_id, detail)
			SELECT 'credential-gateway:' || $2, 'mcp.call', r.work_item_id, $3::jsonb || jsonb_build_object('run', r.id)
			FROM agent_runs r JOIN credential_gateway_grants g USING (run_token)
			WHERE r.run_token = $1 AND g.namespace = $2`, c.RunToken, namespace, detail); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

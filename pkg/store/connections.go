package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Connections of ADR-0195: rows carry secret-store references only
// (ADR-0184), never a value.

var (
	ErrConnectionNotFound = errors.New("connection not found")
	ErrConnectionConflict = errors.New("connection conflicts with a live one")
	ErrRevisionStale      = errors.New("stale revision")
	ErrFlowNotFound       = errors.New("connection flow not found")
)

// ConnectionAudit is one audit_log row a connection change writes.
type ConnectionAudit struct {
	Actor  string
	Action string
	Detail any
}

// ConnectionRow is one row of connections.
type ConnectionRow struct {
	ID             string
	Tenant         string
	Port           string
	Kind           string
	Instance       string
	Method         string
	AccountID      string
	Account        json.RawMessage
	App            string
	InstallationID string
	State          string
	InvalidReason  string
	Secrets        json.RawMessage
	ExpiresAt      *time.Time
	HandOverAgent  bool
	Label          string
	Revision       int64
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedBy      string
	UpdatedAt      time.Time
	ReconnectedBy  string
	ReconnectedAt  *time.Time
	LastProbe      json.RawMessage
	LastProbeAt    *time.Time
	Removal        json.RawMessage
	RemovedAt      *time.Time
}

const connectionColumns = `id, tenant, port, kind, instance, method, account_id, account, COALESCE(app, ''), COALESCE(installation_id, ''),
	state, COALESCE(invalid_reason, ''), secrets, expires_at, hand_over_agent, label, revision, created_by, created_at, updated_by, updated_at,
	COALESCE(reconnected_by, ''), reconnected_at, last_probe, last_probe_at, removal, removed_at`

func scanConnection(row pgx.Row) (ConnectionRow, error) {
	var c ConnectionRow
	err := row.Scan(&c.ID, &c.Tenant, &c.Port, &c.Kind, &c.Instance, &c.Method, &c.AccountID, &c.Account, &c.App, &c.InstallationID,
		&c.State, &c.InvalidReason, &c.Secrets, &c.ExpiresAt, &c.HandOverAgent, &c.Label, &c.Revision, &c.CreatedBy, &c.CreatedAt, &c.UpdatedBy, &c.UpdatedAt,
		&c.ReconnectedBy, &c.ReconnectedAt, &c.LastProbe, &c.LastProbeAt, &c.Removal, &c.RemovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrConnectionNotFound
	}
	return c, err
}

func collectConnections(rows pgx.Rows, err error) ([]ConnectionRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionRow
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func writeConnectionAudit(ctx context.Context, tx pgx.Tx, audits []ConnectionAudit) error {
	for _, a := range audits {
		detail, err := json.Marshal(a.Detail)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, detail) VALUES ($1, $2, $3)`, a.Actor, a.Action, detail); err != nil {
			return err
		}
	}
	return nil
}

// AuditConnection writes audit rows on their own.
func (s *Store) AuditConnection(ctx context.Context, audits ...ConnectionAudit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return writeConnectionAudit(ctx, tx, audits) })
}

// ConnectionIDTaken reports whether any connection, live or removed, has id.
func (s *Store) ConnectionIDTaken(ctx context.Context, tenant, id string) (bool, error) {
	var taken bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM connections WHERE tenant = $1 AND id = $2)`, tenant, id).Scan(&taken)
	return taken, err
}

// CountLiveConnections counts a Tenant's live runtime connections.
func (s *Store) CountLiveConnections(ctx context.Context, tenant string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM connections WHERE tenant = $1 AND removed_at IS NULL`, tenant).Scan(&n)
	return n, err
}

// Connection reads one connection, live or removed.
func (s *Store) Connection(ctx context.Context, tenant, id string) (ConnectionRow, error) {
	return scanConnection(s.pool.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE tenant = $1 AND id = $2`, tenant, id))
}

// LiveConnectionByAccount finds the live connection of one account.
func (s *Store) LiveConnectionByAccount(ctx context.Context, tenant, port, kind, instance, accountID string) (ConnectionRow, error) {
	return scanConnection(s.pool.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections
		WHERE tenant = $1 AND port = $2 AND kind = $3 AND instance = $4 AND account_id = $5 AND removed_at IS NULL`, tenant, port, kind, instance, accountID))
}

// LiveConnectionsByInstallation lists live connections of one App
// installation.
func (s *Store) LiveConnectionsByInstallation(ctx context.Context, installationID string) ([]ConnectionRow, error) {
	return collectConnections(s.pool.Query(ctx, `SELECT `+connectionColumns+` FROM connections WHERE installation_id = $1 AND removed_at IS NULL ORDER BY id`, installationID))
}

// LiveConnections lists a Tenant's live connections of a port, or of every
// port when port is empty, by id.
func (s *Store) LiveConnections(ctx context.Context, tenant, port string) ([]ConnectionRow, error) {
	return collectConnections(s.pool.Query(ctx, `SELECT `+connectionColumns+` FROM connections
		WHERE tenant = $1 AND ($2 = '' OR port = $2) AND removed_at IS NULL ORDER BY id`, tenant, port))
}

func uniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// FlowOutcome is what a completed flow made.
type FlowOutcome struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Status int    `json:"status"`
}

// CreateConnection inserts c, completes its flow and writes audits in one
// transaction. A live connection of the same account or a taken id is
// ErrConnectionConflict.
func (s *Store) CreateConnection(ctx context.Context, c ConnectionRow, flowID string, outcome FlowOutcome, audits []ConnectionAudit) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO connections (id, tenant, port, kind, instance, method, account_id, account, app, installation_id,
			state, invalid_reason, secrets, expires_at, hand_over_agent, label, created_by, updated_by, last_probe, last_probe_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17,$18,$19)`,
			c.ID, c.Tenant, c.Port, c.Kind, c.Instance, c.Method, c.AccountID, c.Account, nullable(c.App), nullable(c.InstallationID),
			c.State, nullable(c.InvalidReason), c.Secrets, c.ExpiresAt, c.HandOverAgent, c.Label, c.CreatedBy, c.LastProbe, c.LastProbeAt)
		if err != nil {
			return err
		}
		if err := completeFlow(ctx, tx, flowID, outcome); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
	if uniqueViolation(err) {
		return ErrConnectionConflict
	}
	return err
}

// ReconnectConnection replaces c's credential, account, state and probe
// under the same id, completes the flow and writes audits.
func (s *Store) ReconnectConnection(ctx context.Context, c ConnectionRow, flowID string, outcome FlowOutcome, audits []ConnectionAudit) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE connections SET method = $3, account = $4, app = $5, installation_id = $6, state = $7, invalid_reason = $8,
			secrets = $9, expires_at = $10, last_probe = $11, last_probe_at = $12, reconnected_by = $13, reconnected_at = now(),
			updated_by = $13, updated_at = now(), revision = revision + 1
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL`,
			c.Tenant, c.ID, c.Method, c.Account, nullable(c.App), nullable(c.InstallationID), c.State, nullable(c.InvalidReason),
			c.Secrets, c.ExpiresAt, c.LastProbe, c.LastProbeAt, c.UpdatedBy)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConnectionNotFound
		}
		if err := completeFlow(ctx, tx, flowID, outcome); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
	if uniqueViolation(err) {
		return ErrConnectionConflict
	}
	return err
}

// SetConnectionState records a probe's state and result, and audits.
func (s *Store) SetConnectionState(ctx context.Context, tenant, id, state, reason string, probe json.RawMessage, audits []ConnectionAudit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connections SET state = $3, invalid_reason = $4, last_probe = COALESCE($5, last_probe),
			last_probe_at = CASE WHEN $5::jsonb IS NULL THEN last_probe_at ELSE now() END
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL`, tenant, id, state, nullable(reason), probe); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
}

// ClaimProbe reserves a probe of id unless one ran within interval.
func (s *Store) ClaimProbe(ctx context.Context, tenant, id string, interval time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE connections SET probe_claimed_at = now()
		WHERE tenant = $1 AND id = $2 AND removed_at IS NULL AND (probe_claimed_at IS NULL OR probe_claimed_at < now() - make_interval(secs => $3))`,
		tenant, id, interval.Seconds())
	return tag.RowsAffected() == 1, err
}

// PatchConnection changes label and hand-over at revision.
func (s *Store) PatchConnection(ctx context.Context, tenant, id string, revision int64, label *string, handOver *bool, actor string, audits []ConnectionAudit) (ConnectionRow, error) {
	var out ConnectionRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := scanConnection(tx.QueryRow(ctx, `UPDATE connections SET label = COALESCE($4, label), hand_over_agent = COALESCE($5, hand_over_agent),
			revision = revision + 1, updated_by = $6, updated_at = now()
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL AND revision = $3 RETURNING `+connectionColumns, tenant, id, revision, label, handOver, actor))
		if errors.Is(err, ErrConnectionNotFound) {
			return ErrRevisionStale
		}
		if err != nil {
			return err
		}
		out = row
		return writeConnectionAudit(ctx, tx, audits)
	})
	return out, err
}

// RefreshConnection runs fn on c under a row lock, so refreshes of one
// connection are serialised across every ploegd, and stores what fn returns
// when it reports a change.
func (s *Store) RefreshConnection(ctx context.Context, tenant, id string, fn func(c ConnectionRow) (secrets json.RawMessage, expires *time.Time, changed bool, err error)) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE tenant = $1 AND id = $2 AND removed_at IS NULL FOR UPDATE`, tenant, id))
		if err != nil {
			return err
		}
		secrets, expires, changed, err := fn(c)
		if err != nil || !changed {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE connections SET secrets = $3, expires_at = $4 WHERE tenant = $1 AND id = $2`, tenant, id, secrets, expires)
		return err
	})
}

// RecordRemoval stores the removal steps done so far.
func (s *Store) RecordRemoval(ctx context.Context, tenant, id string, removal json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE connections SET removal = $3 WHERE tenant = $1 AND id = $2`, tenant, id, removal)
	return err
}

// MarkConnectionRemoved sets removed_at and audits.
func (s *Store) MarkConnectionRemoved(ctx context.Context, tenant, id, actor string, removal json.RawMessage, audits []ConnectionAudit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connections SET removal = $3, removed_at = now(), updated_by = $4, updated_at = now()
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL`, tenant, id, removal, actor); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
}

// ConnectionAppRow is one GitHub App Ploeg created from a manifest.
type ConnectionAppRow struct {
	ID        string
	Tenant    string
	Port      string
	Kind      string
	Instance  string
	AppID     string
	Slug      string
	ClientID  string
	Owner     json.RawMessage
	HTMLURL   string
	Secrets   json.RawMessage
	CreatedBy string
	CreatedAt time.Time
	RemovedAt *time.Time
}

const appColumns = `id, tenant, port, kind, instance, app_id, slug, client_id, owner, html_url, secrets, created_by, created_at, removed_at`

func scanApp(row pgx.Row) (ConnectionAppRow, error) {
	var a ConnectionAppRow
	err := row.Scan(&a.ID, &a.Tenant, &a.Port, &a.Kind, &a.Instance, &a.AppID, &a.Slug, &a.ClientID, &a.Owner, &a.HTMLURL, &a.Secrets, &a.CreatedBy, &a.CreatedAt, &a.RemovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrConnectionNotFound
	}
	return a, err
}

// CreateConnectionApp inserts a, completes its flow, records the follow-on
// installation flow and writes audits in one transaction.
func (s *Store) CreateConnectionApp(ctx context.Context, a ConnectionAppRow, flowID string, outcome FlowOutcome, next *ConnectionFlow, audits []ConnectionAudit) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO connection_apps (id, tenant, port, kind, instance, app_id, slug, client_id, owner, html_url, secrets, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			a.ID, a.Tenant, a.Port, a.Kind, a.Instance, a.AppID, a.Slug, a.ClientID, a.Owner, a.HTMLURL, a.Secrets, a.CreatedBy); err != nil {
			return err
		}
		if err := completeFlow(ctx, tx, flowID, outcome); err != nil {
			return err
		}
		if next != nil {
			if err := insertFlow(ctx, tx, *next); err != nil {
				return err
			}
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
	if uniqueViolation(err) {
		return ErrConnectionConflict
	}
	return err
}

// ConnectionApp reads one App, live or removed.
func (s *Store) ConnectionApp(ctx context.Context, tenant, id string) (ConnectionAppRow, error) {
	return scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM connection_apps WHERE tenant = $1 AND id = $2`, tenant, id))
}

// LiveConnectionAppByAppID finds the live App with GitHub's App id.
func (s *Store) LiveConnectionAppByAppID(ctx context.Context, appID string) (ConnectionAppRow, error) {
	return scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM connection_apps WHERE app_id = $1 AND removed_at IS NULL ORDER BY created_at DESC LIMIT 1`, appID))
}

// LiveConnectionAppIDs lists the vendor App ids of every live connection
// app, so a webhook delivery is matched to an App without a query per
// delivery.
func (s *Store) LiveConnectionAppIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT app_id FROM connection_apps WHERE removed_at IS NULL`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// BindPendingInstallation gives the awaiting_consent connection id, which
// has no installation yet, the installation a signed created delivery
// reported. It reports whether a row was bound.
func (s *Store) BindPendingInstallation(ctx context.Context, tenant, id, installationID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE connections SET installation_id = $3, updated_at = now(), revision = revision + 1
		WHERE tenant = $1 AND id = $2 AND removed_at IS NULL AND installation_id IS NULL AND state = 'awaiting_consent'`, tenant, id, installationID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// LiveConnectionApps lists a Tenant's live Apps by id.
func (s *Store) LiveConnectionApps(ctx context.Context, tenant string) ([]ConnectionAppRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appColumns+` FROM connection_apps WHERE tenant = $1 AND removed_at IS NULL ORDER BY id`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionAppRow
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RemoveConnectionApp marks a removed and audits.
func (s *Store) RemoveConnectionApp(ctx context.Context, tenant, id string, audits []ConnectionAudit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connection_apps SET removed_at = now() WHERE tenant = $1 AND id = $2 AND removed_at IS NULL`, tenant, id); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
}

// ConnectionFlow is one row of connection_flows.
type ConnectionFlow struct {
	ID          string
	StateHash   []byte
	Tenant      string
	Person      string
	Consumer    string
	Method      string
	Request     json.RawMessage
	Verifier    json.RawMessage
	Status      string
	Outcome     *FlowOutcome
	CreatedAt   time.Time
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

const flowColumns = `id, state_hash, tenant, person, consumer, method, request, verifier, status, outcome, created_at, expires_at, completed_at`

func scanFlow(row pgx.Row) (ConnectionFlow, error) {
	var f ConnectionFlow
	err := row.Scan(&f.ID, &f.StateHash, &f.Tenant, &f.Person, &f.Consumer, &f.Method, &f.Request, &f.Verifier, &f.Status, &f.Outcome, &f.CreatedAt, &f.ExpiresAt, &f.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrFlowNotFound
	}
	return f, err
}

func insertFlow(ctx context.Context, tx pgx.Tx, f ConnectionFlow) error {
	_, err := tx.Exec(ctx, `INSERT INTO connection_flows (id, state_hash, tenant, person, consumer, method, request, verifier, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, f.ID, f.StateHash, f.Tenant, f.Person, f.Consumer, f.Method, f.Request, f.Verifier, f.ExpiresAt)
	return err
}

func completeFlow(ctx context.Context, tx pgx.Tx, id string, outcome FlowOutcome) error {
	_, err := tx.Exec(ctx, `UPDATE connection_flows SET status = 'completed', outcome = $2, completed_at = now(), verifier = NULL WHERE id = $1`, id, outcome)
	return err
}

// CreateFlow records a pending flow and audits, refusing when the person
// already has perPerson or the consumer perConsumer open flows.
func (s *Store) CreateFlow(ctx context.Context, f ConnectionFlow, perPerson, perConsumer int, audits []ConnectionAudit) (bool, error) {
	allowed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('connection_flows:' || $1))`, f.Consumer); err != nil {
			return err
		}
		var person, consumer int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE person = $2), count(*) FROM connection_flows
			WHERE consumer = $1 AND status IN ('pending', 'completing') AND expires_at > now()`, f.Consumer, f.Person).Scan(&person, &consumer); err != nil {
			return err
		}
		if person >= perPerson || consumer >= perConsumer {
			return nil
		}
		allowed = true
		if err := insertFlow(ctx, tx, f); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
	return allowed, err
}

// FlowByStateHash finds a flow by the hash of its state.
func (s *Store) FlowByStateHash(ctx context.Context, hash []byte) (ConnectionFlow, error) {
	return scanFlow(s.pool.QueryRow(ctx, `SELECT `+flowColumns+` FROM connection_flows WHERE state_hash = $1`, hash))
}

// Flow reads a flow by its public id.
func (s *Store) Flow(ctx context.Context, id string) (ConnectionFlow, error) {
	return scanFlow(s.pool.QueryRow(ctx, `SELECT `+flowColumns+` FROM connection_flows WHERE id = $1`, id))
}

// ClaimFlow moves a pending, unexpired flow to completing; false means
// another call holds or finished it.
func (s *Store) ClaimFlow(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE connection_flows SET status = 'completing' WHERE id = $1 AND status = 'pending' AND expires_at > now()`, id)
	return tag.RowsAffected() == 1, err
}

// ReleaseFlow returns a completing flow to pending, for a refusal found
// before any vendor call.
func (s *Store) ReleaseFlow(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE connection_flows SET status = 'pending' WHERE id = $1 AND status = 'completing'`, id)
	return err
}

// FailFlow marks a flow failed and audits.
func (s *Store) FailFlow(ctx context.Context, id string, audits []ConnectionAudit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connection_flows SET status = 'failed', completed_at = now(), verifier = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		return writeConnectionAudit(ctx, tx, audits)
	})
}

// EndedFlowVerifiers lists flows that hold a verifier reference but are no
// longer pending, or expired, and clears it.
func (s *Store) EndedFlowVerifiers(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `UPDATE connection_flows SET verifier = NULL
		WHERE verifier IS NOT NULL AND (status IN ('completed', 'failed') OR expires_at < now()) RETURNING id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ReferencedSecretPaths returns every secret-store path a live connection,
// a live App or an open flow references.
func (s *Store) ReferencedSecretPaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v->>'path' FROM connections, jsonb_each(secrets) AS e(k, v) WHERE removed_at IS NULL AND jsonb_typeof(v) = 'object'
		UNION SELECT v->>'path' FROM connection_apps, jsonb_each(secrets) AS e(k, v) WHERE removed_at IS NULL AND jsonb_typeof(v) = 'object'
		UNION SELECT verifier->>'path' FROM connection_flows WHERE verifier IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	paths, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range paths {
		if p != nil {
			out[*p] = true
		}
	}
	return out, nil
}

// ConnectionStateCounts counts live connections per kind and state.
func (s *Store) ConnectionStateCounts(ctx context.Context) (map[[2]string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, state, count(*) FROM connections WHERE removed_at IS NULL GROUP BY kind, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]int{}
	for rows.Next() {
		var kind, state string
		var n int
		if err := rows.Scan(&kind, &state, &n); err != nil {
			return nil, err
		}
		out[[2]string{kind, state}] = n
	}
	return out, rows.Err()
}

// SetConnectionAccount fills the account of a connection stored before
// its installation was approved.
func (s *Store) SetConnectionAccount(ctx context.Context, tenant, id, accountID string, account json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE connections SET account_id = $3, account = $4 WHERE tenant = $1 AND id = $2 AND removed_at IS NULL`, tenant, id, accountID, account)
	if uniqueViolation(err) {
		return ErrConnectionConflict
	}
	return err
}

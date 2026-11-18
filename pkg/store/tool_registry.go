package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

// Audit actions of the tool registry (ADR-0199).
const (
	AuditMCPServerCreated    = "mcp_server.created"
	AuditMCPServerUpdated    = "mcp_server.updated"
	AuditMCPServerRemoved    = "mcp_server.removed"
	AuditSkillSourceCreated  = "skill_source.created"
	AuditSkillSourceUpdated  = "skill_source.updated"
	AuditSkillSourceRemoved  = "skill_source.removed"
	AuditTeamToolsReplaced   = "team_tools.replaced"
	AuditTeamToolsRemoved    = "team_tools.removed"
	AuditToolRegistryRefused = "tool_registry.refused"
)

// ToolAudit is who a registry change is attributed to.
type ToolAudit struct {
	Attribution Attribution
}

func (a ToolAudit) actor() string {
	if a.Attribution.Actor != "" {
		return a.Attribution.Actor
	}
	return "ploegd"
}

func toolAudit(a toolregistry.Audit) ToolAudit {
	switch v := a.(type) {
	case ToolAudit:
		return v
	case *ToolAudit:
		if v != nil {
			return *v
		}
	}
	return ToolAudit{}
}

const toolRegistryWriteLock = `SELECT pg_advisory_xact_lock(hashtextextended('ploeg.tool-registry', 0))`

const mcpServerColumns = `id, tenant, server_key, name, url, transport, auth, credential, tools, adapter, revision`

func scanMCPServer(row pgx.Row) (toolregistry.Server, error) {
	var s toolregistry.Server
	var auth, tools []byte
	var credential []byte
	if err := row.Scan(&s.ID, &s.Tenant, &s.Key, &s.Name, &s.URL, &s.Transport, &auth, &credential, &tools, &s.Adapter, &s.Revision); err != nil {
		return s, err
	}
	if err := json.Unmarshal(auth, &s.Auth); err != nil {
		return s, err
	}
	if err := json.Unmarshal(tools, &s.Tools); err != nil {
		return s, err
	}
	if len(credential) > 0 {
		if err := json.Unmarshal(credential, &s.Credential); err != nil {
			return s, err
		}
	}
	s.Source, s.State = toolregistry.FromRuntime, "active"
	return s, nil
}

func refJSON(ref secretstore.Ref) any {
	if ref.IsZero() {
		return nil
	}
	raw, _ := json.Marshal(ref)
	return raw
}

func sameServer(a, b toolregistry.Server) bool {
	aj, _ := json.Marshal([]any{a.Key, a.Name, a.URL, a.Transport, a.Auth, a.Tools, a.Adapter})
	bj, _ := json.Marshal([]any{b.Key, b.Name, b.URL, b.Transport, b.Auth, b.Tools, b.Adapter})
	return string(aj) == string(bj)
}

// LiveMCPServers are a Tenant's live runtime servers, by key.
func (s *Store) LiveMCPServers(ctx context.Context, tenant string) ([]toolregistry.Server, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mcpServerColumns+` FROM mcp_servers WHERE removed_at IS NULL AND tenant = $1 ORDER BY server_key`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []toolregistry.Server
	for rows.Next() {
		srv, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// CreateMCPServer stores a runtime server. The same key with the same body
// returns the live server with created false and a credential reference
// the caller must discard; another body is a *ToolRegistryExistsError; a
// Tenant at max is toolregistry.ErrLimit.
func (s *Store) CreateMCPServer(ctx context.Context, in toolregistry.Server, max int, audit toolregistry.Audit) (toolregistry.Server, bool, error) {
	var out toolregistry.Server
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		current, err := scanMCPServer(tx.QueryRow(ctx, `SELECT `+mcpServerColumns+` FROM mcp_servers WHERE removed_at IS NULL AND tenant = $1 AND server_key = $2`, in.Tenant, in.Key))
		switch {
		case err == nil && sameServer(current, in):
			out = current
			return nil
		case err == nil:
			return &toolregistry.ExistsError{Revision: current.Revision}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_servers WHERE removed_at IS NULL AND tenant = $1`, in.Tenant).Scan(&live); err != nil {
			return err
		}
		if live >= max {
			return toolregistry.ErrLimit
		}
		auth, _ := json.Marshal(in.Auth)
		tools, _ := json.Marshal(in.Tools)
		out, err = scanMCPServer(tx.QueryRow(ctx, `INSERT INTO mcp_servers (id, tenant, server_key, name, url, transport, auth, credential, tools, adapter, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11) RETURNING `+mcpServerColumns,
			in.ID, in.Tenant, in.Key, in.Name, in.URL, in.Transport, auth, refJSON(in.Credential), tools, in.Adapter, toolAudit(audit).actor()))
		if err != nil {
			return err
		}
		created = true
		return auditToolRegistry(ctx, tx, AuditMCPServerCreated, serverDetail(out), audit)
	})
	return out, created, err
}

// ReplaceMCPServer replaces a live server's body when its revision is
// still revision. A zero in.Credential keeps the stored reference.
func (s *Store) ReplaceMCPServer(ctx context.Context, in toolregistry.Server, revision int64, audit toolregistry.Audit) (toolregistry.Server, toolregistry.Server, error) {
	var before, out toolregistry.Server
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		var err error
		before, err = scanMCPServer(tx.QueryRow(ctx, `SELECT `+mcpServerColumns+` FROM mcp_servers WHERE removed_at IS NULL AND tenant = $1 AND id = $2`, in.Tenant, in.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return toolregistry.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != revision {
			return &toolregistry.StaleError{Revision: before.Revision}
		}
		if in.Key != before.Key {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mcp_servers WHERE removed_at IS NULL AND tenant = $1 AND server_key = $2)`, in.Tenant, in.Key).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return &toolregistry.ExistsError{}
			}
		}
		credential := in.Credential
		if credential.IsZero() {
			credential = before.Credential
		}
		auth, _ := json.Marshal(in.Auth)
		tools, _ := json.Marshal(in.Tools)
		out, err = scanMCPServer(tx.QueryRow(ctx, `UPDATE mcp_servers SET server_key = $3, name = $4, url = $5, transport = $6, auth = $7,
			credential = $8, tools = $9, adapter = $10, updated_by = $11, updated_at = now(), revision = nextval('tool_registry_revisions')
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL RETURNING `+mcpServerColumns,
			in.Tenant, in.ID, in.Key, in.Name, in.URL, in.Transport, auth, refJSON(credential), tools, in.Adapter, toolAudit(audit).actor()))
		if err != nil {
			return err
		}
		detail := serverDetail(out)
		detail["previousRevision"] = before.Revision
		return auditToolRegistry(ctx, tx, AuditMCPServerUpdated, detail, audit)
	})
	return before, out, err
}

// RemoveMCPServer removes a live server unless an attachment names it.
func (s *Store) RemoveMCPServer(ctx context.Context, tenant, id string, audit toolregistry.Audit) (toolregistry.Server, error) {
	var out toolregistry.Server
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		if teams, err := teamsNaming(ctx, tx, tenant, id); err != nil {
			return err
		} else if len(teams) > 0 {
			return &toolregistry.InUseError{Teams: teams}
		}
		var err error
		out, err = scanMCPServer(tx.QueryRow(ctx, `UPDATE mcp_servers SET removed_at = now(), removed_by = $3
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL RETURNING `+mcpServerColumns, tenant, id, toolAudit(audit).actor()))
		if errors.Is(err, pgx.ErrNoRows) {
			return toolregistry.ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditToolRegistry(ctx, tx, AuditMCPServerRemoved, serverDetail(out), audit)
	})
	return out, err
}

func teamsNaming(ctx context.Context, tx pgx.Tx, tenant, id string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT coalesce(team_id, team) FROM team_tools WHERE removed_at IS NULL AND tenant = $1
		AND jsonb_path_exists(roles, '$.*.mcp[*].server ? (@ == $id)', jsonb_build_object('id', $2::text))
		 OR removed_at IS NULL AND tenant = $1 AND jsonb_path_exists(roles, '$.*.skills[*] ? (@ == $id)', jsonb_build_object('id', $2::text))
		ORDER BY 1`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func serverDetail(s toolregistry.Server) map[string]any {
	tools := make([]string, 0, len(s.Tools))
	for _, t := range s.Tools {
		tools = append(tools, t.Name+":"+string(t.Access))
	}
	return map[string]any{"server": map[string]any{"id": s.ID, "key": s.Key, "revision": s.Revision, "host": hostOf(s.URL),
		"authKind": s.Auth.Kind, "credentialSet": !s.Credential.IsZero(), "tools": tools}}
}

func hostOf(raw string) string {
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok {
		rest, _ = strings.CutPrefix(raw, "http://")
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	return host
}

const skillSourceColumns = `id, tenant, source_key, name, kind, ref, url, commit_id, digest, bundle_digest, skills, credential, revision`

func scanSkillSource(row pgx.Row) (toolregistry.SkillSource, error) {
	var s toolregistry.SkillSource
	var skills, credential []byte
	if err := row.Scan(&s.ID, &s.Tenant, &s.Key, &s.Name, &s.Kind, &s.Ref, &s.URL, &s.Commit, &s.Digest, &s.BundleDigest, &skills, &credential, &s.Revision); err != nil {
		return s, err
	}
	if err := json.Unmarshal(skills, &s.Skills); err != nil {
		return s, err
	}
	if len(credential) > 0 {
		if err := json.Unmarshal(credential, &s.Credential); err != nil {
			return s, err
		}
	}
	s.Source, s.State = toolregistry.FromRuntime, "active"
	return s, nil
}

func sameSource(a, b toolregistry.SkillSource) bool {
	return a.Key == b.Key && a.Name == b.Name && a.Kind == b.Kind && a.Ref == b.Ref && a.URL == b.URL && a.Commit == b.Commit && a.Digest == b.Digest
}

// PutSkillBundle stores a verified bundle by its canonical digest; a bundle
// already stored is kept.
func (s *Store) PutSkillBundle(ctx context.Context, digest string, archive []byte, skills []string) error {
	raw, _ := json.Marshal(skills)
	_, err := s.pool.Exec(ctx, `INSERT INTO skill_bundles (digest, archive, skills) VALUES ($1, $2, $3) ON CONFLICT (digest) DO NOTHING`, digest, archive, raw)
	return err
}

// SkillBundle returns a stored bundle's archive, or ErrOperatorNotFound.
func (s *Store) SkillBundle(ctx context.Context, digest string) ([]byte, error) {
	var archive []byte
	err := s.pool.QueryRow(ctx, `SELECT archive FROM skill_bundles WHERE digest = $1`, digest).Scan(&archive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, toolregistry.ErrNotFound
	}
	return archive, err
}

// LiveSkillSources are a Tenant's live runtime skill sources, by key.
func (s *Store) LiveSkillSources(ctx context.Context, tenant string) ([]toolregistry.SkillSource, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+skillSourceColumns+` FROM skill_sources WHERE removed_at IS NULL AND tenant = $1 ORDER BY source_key`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []toolregistry.SkillSource
	for rows.Next() {
		src, err := scanSkillSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// CreateSkillSource stores a runtime skill source whose bundle is stored.
func (s *Store) CreateSkillSource(ctx context.Context, in toolregistry.SkillSource, max int, audit toolregistry.Audit) (toolregistry.SkillSource, bool, error) {
	var out toolregistry.SkillSource
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		current, err := scanSkillSource(tx.QueryRow(ctx, `SELECT `+skillSourceColumns+` FROM skill_sources WHERE removed_at IS NULL AND tenant = $1 AND source_key = $2`, in.Tenant, in.Key))
		switch {
		case err == nil && sameSource(current, in):
			out = current
			return nil
		case err == nil:
			return &toolregistry.ExistsError{Revision: current.Revision}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM skill_sources WHERE removed_at IS NULL AND tenant = $1`, in.Tenant).Scan(&live); err != nil {
			return err
		}
		if live >= max {
			return toolregistry.ErrLimit
		}
		skills, _ := json.Marshal(in.Skills)
		out, err = scanSkillSource(tx.QueryRow(ctx, `INSERT INTO skill_sources (id, tenant, source_key, name, kind, ref, url, commit_id, digest, bundle_digest, skills, credential, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13) RETURNING `+skillSourceColumns,
			in.ID, in.Tenant, in.Key, in.Name, in.Kind, in.Ref, in.URL, in.Commit, in.Digest, in.BundleDigest, skills, refJSON(in.Credential), toolAudit(audit).actor()))
		if err != nil {
			return err
		}
		created = true
		return auditToolRegistry(ctx, tx, AuditSkillSourceCreated, sourceDetail(out), audit)
	})
	return out, created, err
}

// ReplaceSkillSource replaces a live source when its revision is still
// revision. A zero in.Credential keeps the stored reference.
func (s *Store) ReplaceSkillSource(ctx context.Context, in toolregistry.SkillSource, revision int64, audit toolregistry.Audit) (toolregistry.SkillSource, toolregistry.SkillSource, error) {
	var before, out toolregistry.SkillSource
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		var err error
		before, err = scanSkillSource(tx.QueryRow(ctx, `SELECT `+skillSourceColumns+` FROM skill_sources WHERE removed_at IS NULL AND tenant = $1 AND id = $2`, in.Tenant, in.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return toolregistry.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != revision {
			return &toolregistry.StaleError{Revision: before.Revision}
		}
		if in.Key != before.Key {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM skill_sources WHERE removed_at IS NULL AND tenant = $1 AND source_key = $2)`, in.Tenant, in.Key).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return &toolregistry.ExistsError{}
			}
		}
		credential := in.Credential
		if credential.IsZero() {
			credential = before.Credential
		}
		skills, _ := json.Marshal(in.Skills)
		out, err = scanSkillSource(tx.QueryRow(ctx, `UPDATE skill_sources SET source_key = $3, name = $4, kind = $5, ref = $6, url = $7, commit_id = $8,
			digest = $9, bundle_digest = $10, skills = $11, credential = $12, updated_by = $13, updated_at = now(), revision = nextval('tool_registry_revisions')
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL RETURNING `+skillSourceColumns,
			in.Tenant, in.ID, in.Key, in.Name, in.Kind, in.Ref, in.URL, in.Commit, in.Digest, in.BundleDigest, skills, refJSON(credential), toolAudit(audit).actor()))
		if err != nil {
			return err
		}
		detail := sourceDetail(out)
		detail["previousRevision"] = before.Revision
		return auditToolRegistry(ctx, tx, AuditSkillSourceUpdated, detail, audit)
	})
	return before, out, err
}

// RemoveSkillSource removes a live source unless an attachment names it.
func (s *Store) RemoveSkillSource(ctx context.Context, tenant, id string, audit toolregistry.Audit) (toolregistry.SkillSource, error) {
	var out toolregistry.SkillSource
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		if teams, err := teamsNaming(ctx, tx, tenant, id); err != nil {
			return err
		} else if len(teams) > 0 {
			return &toolregistry.InUseError{Teams: teams}
		}
		var err error
		out, err = scanSkillSource(tx.QueryRow(ctx, `UPDATE skill_sources SET removed_at = now(), removed_by = $3
			WHERE tenant = $1 AND id = $2 AND removed_at IS NULL RETURNING `+skillSourceColumns, tenant, id, toolAudit(audit).actor()))
		if errors.Is(err, pgx.ErrNoRows) {
			return toolregistry.ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditToolRegistry(ctx, tx, AuditSkillSourceRemoved, sourceDetail(out), audit)
	})
	return out, err
}

func sourceDetail(s toolregistry.SkillSource) map[string]any {
	return map[string]any{"skillSource": map[string]any{"id": s.ID, "key": s.Key, "revision": s.Revision, "kind": s.Kind,
		"digest": s.Digest, "bundleDigest": s.BundleDigest, "skills": s.Skills, "credentialSet": !s.Credential.IsZero()}}
}

const teamToolsColumns = `tenant, team, coalesce(team_id, ''), roles, revision`

func scanTeamTools(row pgx.Row) (toolregistry.Attachment, error) {
	var a toolregistry.Attachment
	var roles []byte
	if err := row.Scan(&a.Tenant, &a.Team, &a.TeamID, &roles, &a.Revision); err != nil {
		return a, err
	}
	a.Source = toolregistry.FromRuntime
	return a, json.Unmarshal(roles, &a.Roles)
}

func teamWhere(team toolregistry.Team) (string, []any) {
	return `(team_id = $2 OR team_id IS NULL AND lower(team) = lower($3))`, []any{team.ID, team.Key}
}

// TeamTools is a Team's live runtime attachment, or ErrOperatorNotFound.
func (s *Store) TeamTools(ctx context.Context, tenant string, team toolregistry.Team) (toolregistry.Attachment, error) {
	where, args := teamWhere(team)
	a, err := scanTeamTools(s.pool.QueryRow(ctx, `SELECT `+teamToolsColumns+` FROM team_tools WHERE removed_at IS NULL AND tenant = $1 AND `+where,
		append([]any{tenant}, args...)...))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, toolregistry.ErrNotFound
	}
	return a, err
}

// LiveTeamTools are a Tenant's live runtime attachments.
func (s *Store) LiveTeamTools(ctx context.Context, tenant string) ([]toolregistry.Attachment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+teamToolsColumns+` FROM team_tools WHERE removed_at IS NULL AND tenant = $1 ORDER BY team`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []toolregistry.Attachment
	for rows.Next() {
		a, err := scanTeamTools(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReplaceTeamTools replaces a Team's attachment. revision nil creates one
// and is a *ToolRegistryStaleError when one exists; otherwise the live
// attachment's revision must equal *revision.
func (s *Store) ReplaceTeamTools(ctx context.Context, tenant string, team toolregistry.Team, roles map[string]toolregistry.RoleTools, revision *int64, audit toolregistry.Audit) (toolregistry.Attachment, error) {
	raw, err := json.Marshal(roles)
	if err != nil {
		return toolregistry.Attachment{}, err
	}
	var out toolregistry.Attachment
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		where, args := teamWhere(team)
		current, err := scanTeamTools(tx.QueryRow(ctx, `SELECT `+teamToolsColumns+` FROM team_tools WHERE removed_at IS NULL AND tenant = $1 AND `+where+` FOR UPDATE`,
			append([]any{tenant}, args...)...))
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case revision == nil && exists, revision != nil && !exists, revision != nil && current.Revision != *revision:
			return &toolregistry.StaleError{Revision: current.Revision}
		}
		var teamID any
		if team.ID != "" {
			teamID = team.ID
		}
		if exists {
			out, err = scanTeamTools(tx.QueryRow(ctx, `UPDATE team_tools SET roles = $4, team = $5, team_id = $6, updated_by = $7, updated_at = now(),
				revision = nextval('tool_registry_revisions') WHERE removed_at IS NULL AND tenant = $1 AND `+where+` RETURNING `+teamToolsColumns,
				append([]any{tenant}, append(args, raw, team.Key, teamID, toolAudit(audit).actor())...)...))
		} else {
			out, err = scanTeamTools(tx.QueryRow(ctx, `INSERT INTO team_tools (tenant, team, team_id, roles, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $5, $5) RETURNING `+teamToolsColumns, tenant, team.Key, teamID, raw, toolAudit(audit).actor()))
		}
		if err != nil {
			return err
		}
		return auditToolRegistry(ctx, tx, AuditTeamToolsReplaced, attachmentDetail(out), audit)
	})
	return out, err
}

// RemoveTeamTools removes a Team's attachment; none is not an error.
func (s *Store) RemoveTeamTools(ctx context.Context, tenant string, team toolregistry.Team, audit toolregistry.Audit) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, toolRegistryWriteLock); err != nil {
			return err
		}
		where, args := teamWhere(team)
		out, err := scanTeamTools(tx.QueryRow(ctx, `UPDATE team_tools SET removed_at = now(), removed_by = $4
			WHERE removed_at IS NULL AND tenant = $1 AND `+where+` RETURNING `+teamToolsColumns, append([]any{tenant}, append(args, toolAudit(audit).actor())...)...))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return auditToolRegistry(ctx, tx, AuditTeamToolsRemoved, attachmentDetail(out), audit)
	})
}

func attachmentDetail(a toolregistry.Attachment) map[string]any {
	roles := make([]string, 0, len(a.Roles))
	for r := range a.Roles {
		roles = append(roles, r)
	}
	slices.Sort(roles)
	return map[string]any{"teamTools": map[string]any{"team": a.Team, "teamId": a.TeamID, "revision": a.Revision, "roles": roles}}
}

// ToolRegistryRefused records a refused registry change.
func (s *Store) ToolRegistryRefused(ctx context.Context, code, route string, audit toolregistry.Audit) error {
	return auditToolRegistry(ctx, s.pool, AuditToolRegistryRefused, map[string]any{"code": code, "route": route}, audit)
}

func auditToolRegistry(ctx context.Context, db execer, action string, detail map[string]any, a toolregistry.Audit) error {
	audit := toolAudit(a)
	if audit.Attribution.Consumer != "" {
		detail["consumer"] = audit.Attribution.Consumer
		detail["channel"] = channelOf(audit.Attribution)
		detail[AuditAttributionKey] = audit.Attribution
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_log (actor, action, detail) VALUES ($1, $2, $3)`, toolAudit(audit).actor(), action, raw)
	return err
}

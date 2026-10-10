package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Limits of one Work Item's delivery facts and of the facts list
// (ADR-0079).
const (
	FactsListLimit        = 25
	DefaultFactsListLimit = 10
	FactsListMembers      = 20

	factsShiftLimit      = 200
	factsRunLimit        = 1000
	factsPlayLimit       = 50
	factsTransitionLimit = 1000
	factsCheckpointLimit = 200
)

// ErrInvalidFactsCursor is returned for a facts list cursor Ploeg did not
// issue.
var ErrInvalidFactsCursor = errors.New("invalid facts list cursor")

// LiveUsage is what the gateway has recorded so far for one running Run
// (ADR-0049).
type LiveUsage struct {
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
}

// FactsOptions is what reading delivery facts takes from configuration.
type FactsOptions struct {
	// Bots are the forge logins Ploeg acts as, reported as botLogins.
	Bots []string
	// Live reads what the gateway recorded so far for one running Run. Nil
	// leaves liveUsage empty.
	Live func(ctx context.Context, runToken string) (LiveUsage, error)
	// TaskURL names the tracker page of a Work Item stored without one.
	TaskURL func(provider, externalID string) string
	// Now is the clock of liveUsage's observedAt; zero means time.Now.
	Now time.Time
}

// WorkItemFacts is everything Ploeg stored about one Work Item's delivery,
// with no derived figure (ADR-0079). The nested collections are assembled
// in SQL as the operator contract's JSON and kept raw.
type WorkItemFacts struct {
	WorkItem          FactsWorkItem     `json:"workItem"`
	ActivityAt        time.Time         `json:"activityAt"`
	Shifts            []OperatorShift   `json:"shifts"`
	Runs              []OperatorRun     `json:"runs"`
	LiveUsage         []FactsLiveUsage  `json:"liveUsage"`
	PullRequests      []json.RawMessage `json:"pullRequests"`
	StatusTransitions []json.RawMessage `json:"statusTransitions"`
	GateTransitions   []json.RawMessage `json:"gateTransitions"`
	Checkpoints       []json.RawMessage `json:"checkpoints"`
	RunBudgetHolds    []json.RawMessage `json:"runBudgetHolds"`
	// Asks are the Work Item's Asks (ADR-0081). They are not delivery: Runs,
	// LiveUsage, RunBudgetHolds and ActivityAt leave their Runs out.
	Asks               []json.RawMessage  `json:"asks"`
	DeployEnvironments []json.RawMessage  `json:"deployEnvironments"`
	Roster             []FactsRosterEntry `json:"roster"`
	BotLogins          []string           `json:"botLogins"`
	Truncated          FactsTruncated     `json:"truncated"`
}

// FactsWorkItem is the Work Item as Ploeg stored it.
type FactsWorkItem struct {
	ID               string            `json:"id"`
	Provider         string            `json:"provider"`
	ExternalID       string            `json:"externalId"`
	ExternalRef      string            `json:"externalRef"`
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	State            string            `json:"state"`
	Team             string            `json:"team"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	TrackerCreatedAt *time.Time        `json:"trackerCreatedAt"`
	EstimateSeconds  *int64            `json:"estimateSeconds"`
	ExternalScope    string            `json:"externalScope"`
	AdmittedAt       *time.Time        `json:"admittedAt"`
	Target           *FactsTarget      `json:"target"`
	Epics            []json.RawMessage `json:"epics"`
	Withdrawals      []json.RawMessage `json:"withdrawals"`
}

// FactsTarget is the repository a Work Item's pull requests go to.
type FactsTarget struct {
	Forge      string `json:"forge"`
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	BaseBranch string `json:"baseBranch"`
}

// FactsLiveUsage is what the gateway recorded so far for one running Run.
type FactsLiveUsage struct {
	RunID        string    `json:"runId"`
	ObservedAt   time.Time `json:"observedAt"`
	CostUSD      float64   `json:"costUsd"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
}

// FactsRosterEntry is a login the stored facts name and what it did:
// merger, reviewer, author, pusher, commenter or mover, in that order.
type FactsRosterEntry struct {
	Login string   `json:"login"`
	Roles []string `json:"roles"`
}

// FactsTruncated says which collections hit their bound.
type FactsTruncated struct {
	Shifts            bool `json:"shifts"`
	Runs              bool `json:"runs"`
	PullRequests      bool `json:"pullRequests"`
	StatusTransitions bool `json:"statusTransitions"`
	GateTransitions   bool `json:"gateTransitions"`
	Checkpoints       bool `json:"checkpoints"`
	Asks              bool `json:"asks"`
}

var factsRoles = []string{"merger", "reviewer", "author", "pusher", "commenter", "mover"}

const factsActivity = `GREATEST(
	(SELECT max(GREATEST(r.started_at, r.finished_at)) FROM agent_runs r WHERE r.work_item_id = i.id AND r.role <> 'ask'),
	(SELECT max(GREATEST(p.first_seen_at, p.opened_at, p.merged_at, p.closed_at)) FROM pull_requests p WHERE p.work_item_id = i.id),
	(SELECT max(v.received_at) FROM pull_request_reviews v JOIN pull_requests p ON p.id = v.pull_request_id WHERE p.work_item_id = i.id),
	(SELECT max(e.at) FROM pull_request_events e JOIN pull_requests p ON p.id = e.pull_request_id WHERE p.work_item_id = i.id),
	(SELECT max(pd.first_deployed_at) FROM pull_request_deployments pd JOIN pull_requests p ON p.id = pd.pull_request_id WHERE p.work_item_id = i.id),
	(SELECT max(st.at) FROM status_transitions st WHERE st.work_item_id = i.id),
	(SELECT max(g.at) FROM gate_transitions g WHERE g.work_item_id = i.id),
	i.created_at)`

const factsAskJSON = `jsonb_build_object('runId', r.id::text,
	'state', CASE WHEN k.finished_at IS NOT NULL THEN 'finished'
	              WHEN r.state = 'running' AND r.expires_at > now() THEN 'open' ELSE 'expired' END,
	'budgetUsd', COALESCE(a.authorized, r.authorized),
	'costStatus', CASE WHEN a.state = 'reconciled' AND NOT a.cost_known THEN 'unknown'
	                   WHEN a.state = 'reconciled' AND COALESCE(a.corrections_until > now(), false) THEN 'provisional'
	                   WHEN a.state = 'reconciled' THEN 'settled' ELSE 'provisional' END,
	'usd', CASE WHEN a.state = 'reconciled' THEN a.reconciled_spend ELSE a.observed_spend END,
	'createdAt', k.created_at, 'finishedAt', k.finished_at)`

const factsPeople = `SELECT p.work_item_id AS id, p.merged_by AS login, 1 AS role FROM pull_requests p WHERE COALESCE(p.merged_by, '') <> ''
	UNION ALL SELECT p.work_item_id, v.reviewer, 2 FROM pull_request_reviews v JOIN pull_requests p ON p.id = v.pull_request_id WHERE v.reviewer <> ''
	UNION ALL SELECT p.work_item_id, p.author, 3 FROM pull_requests p WHERE COALESCE(p.author, '') <> ''
	UNION ALL SELECT p.work_item_id, e.actor, 4 FROM pull_request_events e JOIN pull_requests p ON p.id = e.pull_request_id
		WHERE e.kind IN ('push', 'force_push') AND e.actor <> ''
	UNION ALL SELECT p.work_item_id, e.actor, 5 FROM pull_request_events e JOIN pull_requests p ON p.id = e.pull_request_id
		WHERE e.kind IN ('comment', 'review_comment') AND e.actor <> ''
	UNION ALL SELECT g.work_item_id, g.actor, 6 FROM gate_transitions g WHERE COALESCE(g.actor, '') <> ''`

const factsPlayRef = `jsonb_build_object('id', x.id::text, 'forge', x.forge, 'owner', x.repo_owner, 'repo', x.repo_name, 'number', x.number)`

const factsPullRequestURL = `COALESCE(
	(SELECT left(c.pr_url, 4096) FROM checkpoints c
	 WHERE c.work_item_id = p.work_item_id AND c.pr_url ~ ('/(pulls?|merge_requests)/' || p.number || '/?$')
	   AND position('/' || lower(p.repo_owner || '/' || p.repo_name) || '/' IN lower(c.pr_url)) > 0
	 ORDER BY c.created_at, c.id LIMIT 1),
	(SELECT left(l.link, 4096) FROM agent_runs r CROSS JOIN LATERAL unnest(r.links[1:30]) WITH ORDINALITY AS l(link, ord)
	 WHERE r.work_item_id = p.work_item_id AND l.link ~ ('/(pulls?|merge_requests)/' || p.number || '/?$')
	   AND position('/' || lower(p.repo_owner || '/' || p.repo_name) || '/' IN lower(l.link)) > 0
	 ORDER BY r.id, l.ord LIMIT 1),
	'')`

const factsPullRequestJSON = `jsonb_build_object(
	'id', p.id::text, 'forge', p.forge, 'owner', p.repo_owner, 'repo', p.repo_name, 'number', p.number,
	'url', ` + factsPullRequestURL + `,
	'shiftId', p.shift_id::text, 'branch', left(COALESCE(p.branch, sh.branch), 1024), 'baseBranch', left(p.base_branch, 1024),
	'state', p.state, 'draft', p.draft, 'author', left(p.author, 256),
	'headSha', left(p.head_sha, 128), 'mergeCommitSha', left(p.merge_commit_sha, 128),
	'openedAt', p.opened_at, 'firstSeenAt', p.first_seen_at, 'mergedAt', p.merged_at, 'mergedBy', left(p.merged_by, 256),
	'closedAt', p.closed_at, 'updatedAt', p.updated_at,
	'additions', p.additions, 'deletions', p.deletions, 'changedFiles', p.changed_files,
	'labels', to_jsonb(p.labels[1:100]),
	'commits', p.commits, 'firstCommitAt', p.first_commit_at, 'forcePushes', p.force_pushes,
	'activityCapturedAt', p.activity_captured_at, 'activityTruncated', p.activity_truncated,
	'mergeState', p.merge_state, 'mergeHeadSha', left(p.merge_head_sha, 128), 'mergeCheckedAt', p.merge_checked_at,
	'unmergeablePolls', p.unmergeable_polls,
	'commitStatus', CASE WHEN p.ci_state IS NOT NULL AND p.ci_captured_at IS NOT NULL THEN jsonb_build_object(
		'state', p.ci_state, 'checks', COALESCE(p.ci_checks, '[]'::jsonb), 'headSha', COALESCE(left(p.ci_head_sha, 128), ''),
		'capturedAt', p.ci_captured_at) END,
	'ciRunsCapturedAt', p.ci_runs_captured_at, 'ciRunsSource', p.ci_runs_source, 'ciRunsTruncated', p.ci_runs_truncated,
	'ciRuns', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'key', c.run_key, 'headSha', left(c.head_sha, 128), 'workflow', left(c.workflow, 256), 'status', c.status,
			'createdAt', c.created_at, 'startedAt', c.started_at, 'completedAt', c.completed_at, 'jobs', c.jobs)
			ORDER BY c.created_at NULLS LAST, c.id), '[]'::jsonb)
		FROM (SELECT * FROM pull_request_ci_runs WHERE pull_request_id = p.id ORDER BY id LIMIT 30) c),
	'reviews', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'reviewer', left(v.reviewer, 256), 'state', v.state, 'headSha', left(v.head_sha, 128), 'receivedAt', v.received_at)
			ORDER BY v.received_at, v.id), '[]'::jsonb)
		FROM (SELECT * FROM pull_request_reviews WHERE pull_request_id = p.id ORDER BY id LIMIT 500) v),
	'events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'kind', e.kind, 'actor', left(e.actor, 256), 'at', e.at, 'state', e.state, 'headSha', left(e.head_sha, 128))
			ORDER BY e.at, e.id), '[]'::jsonb)
		FROM (SELECT * FROM pull_request_events WHERE pull_request_id = p.id ORDER BY at, id LIMIT 500) e),
	'filesCapturedAt', p.files_captured_at, 'filesTruncated', p.files_truncated,
	'files', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'path', f.path, 'additions', f.additions, 'deletions', f.deletions,
			'indentation', CASE WHEN f.indent_method IS NOT NULL THEN jsonb_build_object(
				'method', f.indent_method, 'unit', f.indent_unit, 'added', f.indent_added, 'removed', f.indent_removed,
				'maxDepth', f.indent_max_depth) END)
			ORDER BY f.path COLLATE "C"), '[]'::jsonb)
		FROM (SELECT * FROM pull_request_files WHERE pull_request_id = p.id ORDER BY path COLLATE "C" LIMIT 300) f),
	'changedPaths', (SELECT jsonb_build_object('headSha', pc.head_sha, 'truncated', pc.truncated, 'capturedAt', pc.captured_at,
			'paths', (SELECT COALESCE(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
					'path', pp.path, 'status', pp.status, 'previousPath', pp.previous_path)) ORDER BY pp.position), '[]'::jsonb)
				FROM pull_request_paths pp WHERE pp.pull_request_id = pc.pull_request_id AND pp.head_sha = pc.head_sha))
		FROM pull_request_path_captures pc WHERE pc.pull_request_id = p.id ORDER BY pc.captured_at DESC LIMIT 1),
	'reverts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'forge', rv.forge, 'owner', rv.repo_owner, 'repo', rv.repo_name, 'number', rv.number,
			'mergeCommitSha', left(rv.merge_commit_sha, 128), 'mergedAt', rv.merged_at, 'mergedBy', left(rv.merged_by, 256),
			'matchedBy', rv.matched_by, 'detectedAt', rv.detected_at) ORDER BY rv.detected_at, rv.number), '[]'::jsonb)
		FROM pull_request_reverts rv WHERE rv.pull_request_id = p.id),
	'deployments', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'environment', pd.environment, 'firstDeployedAt', pd.first_deployed_at, 'sha', d.sha, 'deployedAt', d.deployed_at,
			'url', left(d.url, 2048), 'source', d.source) ORDER BY pd.first_deployed_at, pd.environment), '[]'::jsonb)
		FROM pull_request_deployments pd JOIN deployments d ON d.id = pd.deployment_id WHERE pd.pull_request_id = p.id))`

type factsRunning struct {
	id    int64
	token string
}

// WorkItemFacts reads the delivery facts of Work Item id within teams (nil
// means every team) in one read-only snapshot (ADR-0079).
func (s *Store) WorkItemFacts(ctx context.Context, id int64, teams []string, opts FactsOptions) (WorkItemFacts, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return WorkItemFacts{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	f := WorkItemFacts{BotLogins: factsBots(opts.Bots), LiveUsage: []FactsLiveUsage{}, Roster: []FactsRosterEntry{}}
	item := &f.WorkItem
	var forge, owner, repo, base string
	err = tx.QueryRow(ctx, `SELECT i.id::text, i.provider, i.external_id, left(i.url, 4096), left(i.title, 4096), i.state, i.team,
		i.created_at, i.updated_at, i.tracker_created_at, i.estimate_seconds, i.external_scope,
		(SELECT a.at FROM audit_log a WHERE a.work_item_id = i.id AND a.action IN ('work_item.queued', 'work_item.approved')
		 ORDER BY a.id LIMIT 1),
		i.target_forge, i.target_owner, i.target_repo, i.target_base_branch, `+factsActivity+`
		FROM work_items i WHERE i.id = $1 AND ($2::text[] IS NULL OR i.team = ANY($2))`, id, teams).
		Scan(&item.ID, &item.Provider, &item.ExternalID, &item.URL, &item.Title, &item.State, &item.Team,
			&item.CreatedAt, &item.UpdatedAt, &item.TrackerCreatedAt, &item.EstimateSeconds, &item.ExternalScope, &item.AdmittedAt, &forge, &owner, &repo, &base, &f.ActivityAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkItemFacts{}, ErrOperatorNotFound
	}
	if err != nil {
		return WorkItemFacts{}, err
	}
	if item.Provider != "manual" && item.ExternalID != "" {
		item.ExternalRef = work.Reference(work.WorkItem{Provider: item.Provider, ExternalID: item.ExternalID})
	}
	if item.URL == "" && opts.TaskURL != nil {
		item.URL = opts.TaskURL(item.Provider, item.ExternalID)
	}
	if forge != "" && owner != "" && repo != "" {
		item.Target = &FactsTarget{Forge: forge, Owner: owner, Repo: repo, BaseBranch: base}
	}

	var shifts, runs []json.RawMessage
	queries := []struct {
		into  *[]json.RawMessage
		limit int
		cut   *bool
		sql   string
	}{
		{&item.Epics, 50, nil, `SELECT jsonb_build_object('provider', e.provider, 'externalId', e.epic_external_id,
			'title', left(e.epic_title, 4096), 'firstSeenAt', e.first_seen_at, 'lastSeenAt', e.last_seen_at, 'removedAt', e.removed_at)
			FROM work_item_epics e WHERE e.work_item_id = $1 ORDER BY e.first_seen_at, e.epic_external_id LIMIT $2`},
		{&item.Withdrawals, 20, nil, `SELECT jsonb_build_object('at', a.at, 'actor', left(a.actor, 256))
			FROM audit_log a WHERE a.work_item_id = $1 AND a.action = 'work_item.withdrawn' ORDER BY a.id LIMIT $2`},
		{&shifts, factsShiftLimit, &f.Truncated.Shifts, `SELECT ` + operatorShiftJSON + `
			FROM shifts sh WHERE sh.work_item_id = $1 ORDER BY sh.id LIMIT $2`},
		{&runs, factsRunLimit, &f.Truncated.Runs, `SELECT ` + operatorRunJSON + `
			FROM agent_runs r WHERE r.work_item_id = $1 AND r.role <> 'ask' ORDER BY r.id LIMIT $2`},
		{&f.PullRequests, factsPlayLimit, &f.Truncated.PullRequests, `SELECT ` + factsPullRequestJSON + `
			FROM pull_requests p LEFT JOIN shifts sh ON sh.id = p.shift_id
			WHERE p.work_item_id = $1 ORDER BY p.first_seen_at, p.id LIMIT $2`},
		{&f.StatusTransitions, factsTransitionLimit, &f.Truncated.StatusTransitions, `SELECT jsonb_build_object(
			'status', left(st.status, 256), 'gate', st.gate, 'at', st.at, 'observed', st.observed, 'receivedAt', st.received_at)
			FROM status_transitions st WHERE st.work_item_id = $1 ORDER BY st.id LIMIT $2`},
		{&f.GateTransitions, factsTransitionLimit, &f.Truncated.GateTransitions, `SELECT jsonb_build_object(
			'gate', g.gate, 'status', left(g.status, 256), 'actor', left(g.actor, 256), 'reason', g.reason, 'at', g.at,
			'receivedAt', g.received_at)
			FROM gate_transitions g WHERE g.work_item_id = $1 ORDER BY g.id LIMIT $2`},
		{&f.Checkpoints, factsCheckpointLimit, &f.Truncated.Checkpoints, `SELECT jsonb_build_object(
			'id', c.id::text, 'phase', c.phase, 'branch', left(c.branch, 1024), 'prUrl', left(c.pr_url, 4096), 'createdAt', c.created_at)
			FROM checkpoints c WHERE c.work_item_id = $1 AND c.pr_url <> '' ORDER BY c.created_at, c.id LIMIT $2`},
		{&f.RunBudgetHolds, factsRunLimit, nil, `SELECT jsonb_build_object('runId', r.id::text, 'shiftId', r.shift_id::text, 'reservedUsd', h.reserved)
			FROM agent_runs r JOIN run_budget_holds h ON h.run_token = r.run_token
			WHERE r.work_item_id = $1 AND r.role <> 'ask' AND h.reserved > 0 ORDER BY r.id LIMIT $2`},
		{&f.Asks, factsRunLimit, &f.Truncated.Asks, `SELECT ` + factsAskJSON + `
			FROM asks k JOIN agent_runs r ON r.id = k.run_id LEFT JOIN run_llm_accounts a ON a.run_token = r.run_token
			WHERE k.work_item_id = $1 ORDER BY k.run_id LIMIT $2`},
		{&f.DeployEnvironments, 200, nil, `SELECT jsonb_build_object('forge', d.forge, 'owner', d.repo_owner, 'repo', d.repo_name,
			'environment', d.environment, 'firstDeployedAt', min(d.deployed_at))
			FROM deployments d
			WHERE EXISTS (SELECT 1 FROM pull_requests p WHERE p.work_item_id = $1 AND p.forge = d.forge
				AND lower(p.repo_owner) = d.repo_owner AND lower(p.repo_name) = d.repo_name)
			GROUP BY d.forge, d.repo_owner, d.repo_name, d.environment
			ORDER BY d.forge, d.repo_owner, d.repo_name, d.environment LIMIT $2`},
	}
	for _, q := range queries {
		rows, err := factsRows(ctx, tx, q.sql, id, q.limit+1)
		if err != nil {
			return WorkItemFacts{}, err
		}
		if len(rows) > q.limit {
			rows = rows[:q.limit]
			if q.cut != nil {
				*q.cut = true
			}
		}
		*q.into = rows
	}
	if f.Shifts, err = factsDecode[OperatorShift](shifts); err != nil {
		return WorkItemFacts{}, err
	}
	if f.Runs, err = factsDecode[OperatorRun](runs); err != nil {
		return WorkItemFacts{}, err
	}
	if f.Roster, err = factsRoster(ctx, tx, id); err != nil {
		return WorkItemFacts{}, err
	}
	running, err := factsRunningRuns(ctx, tx, id)
	if err != nil {
		return WorkItemFacts{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkItemFacts{}, err
	}
	f.LiveUsage = factsLive(ctx, running, opts)
	f.ActivityAt = f.ActivityAt.UTC()

	raw, err := json.Marshal(f)
	if err != nil {
		return WorkItemFacts{}, err
	}
	var clean WorkItemFacts
	if err := operatorDecode(raw, &clean); err != nil {
		return WorkItemFacts{}, err
	}
	return clean, nil
}

func factsRows(ctx context.Context, tx pgx.Tx, sql string, id int64, limit int) ([]json.RawMessage, error) {
	rows, err := tx.Query(ctx, sql, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

func factsDecode[T any](rows []json.RawMessage) ([]T, error) {
	out := make([]T, 0, len(rows))
	for _, raw := range rows {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func factsRoster(ctx context.Context, tx pgx.Tx, id int64) ([]FactsRosterEntry, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT left(login, 256), role FROM (`+factsPeople+`) people WHERE id = $1 LIMIT 2000`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := map[string]map[int]bool{}
	for rows.Next() {
		var login string
		var role int
		if err := rows.Scan(&login, &role); err != nil {
			return nil, err
		}
		if roles[login] == nil {
			roles[login] = map[int]bool{}
		}
		roles[login][role] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	logins := make([]string, 0, len(roles))
	for login := range roles {
		logins = append(logins, login)
	}
	sort.Strings(logins)
	out := make([]FactsRosterEntry, 0, len(logins))
	for _, login := range logins {
		entry := FactsRosterEntry{Login: login, Roles: []string{}}
		for i, name := range factsRoles {
			if roles[login][i+1] {
				entry.Roles = append(entry.Roles, name)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func factsRunningRuns(ctx context.Context, tx pgx.Tx, id int64) ([]factsRunning, error) {
	rows, err := tx.Query(ctx, `SELECT id, run_token FROM agent_runs
		WHERE work_item_id = $1 AND state = 'running' AND role <> 'ask' AND started_at IS NOT NULL AND finished_at IS NULL ORDER BY id LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []factsRunning
	for rows.Next() {
		var r factsRunning
		if err := rows.Scan(&r.id, &r.token); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func factsLive(ctx context.Context, running []factsRunning, opts FactsOptions) []FactsLiveUsage {
	out := []FactsLiveUsage{}
	if opts.Live == nil {
		return out
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	for _, r := range running {
		if r.token == "" {
			continue
		}
		usage, err := opts.Live(ctx, r.token)
		if err != nil || !validSpend(usage.CostUSD) || usage.InputTokens < 0 || usage.OutputTokens < 0 {
			continue
		}
		out = append(out, FactsLiveUsage{RunID: strconv.FormatInt(r.id, 10), ObservedAt: now.UTC(), CostUSD: usage.CostUSD,
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens})
	}
	return out
}

func factsBots(bots []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, b := range bots {
		l := strings.ToLower(strings.TrimSpace(b))
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// FactsListFilter selects the Work Items of WorkItemFactsList. Teams is the
// consumer's scope (nil means every team) and Team narrows it. Members, when
// not empty, keeps the Work Items whose roster names one of them, compared
// without case. Since keeps activity at or after it; Before continues after
// the cursor of an earlier page.
type FactsListFilter struct {
	Teams   []string
	Team    string
	Members []string
	Since   *time.Time
	Before  *FactsCursor
	Limit   int
}

// FactsCursor is a position in the facts list: the activity time and Work
// Item of the last entry of a page.
type FactsCursor struct {
	At         time.Time
	WorkItemID int64
}

const factsCursorPrefix = "f1."

// String encodes c as the opaque nextBefore of the facts list.
func (c FactsCursor) String() string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + strconv.FormatInt(c.WorkItemID, 10)
	return factsCursorPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseFactsCursor decodes a nextBefore that FactsCursor.String issued.
func ParseFactsCursor(s string) (FactsCursor, error) {
	encoded, ok := strings.CutPrefix(s, factsCursorPrefix)
	if !ok || len(encoded) > 64 {
		return FactsCursor{}, ErrInvalidFactsCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return FactsCursor{}, ErrInvalidFactsCursor
	}
	at, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return FactsCursor{}, ErrInvalidFactsCursor
	}
	micros, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return FactsCursor{}, ErrInvalidFactsCursor
	}
	workItem, err := strconv.ParseInt(id, 10, 64)
	if err != nil || workItem <= 0 {
		return FactsCursor{}, ErrInvalidFactsCursor
	}
	return FactsCursor{At: time.UnixMicro(micros).UTC(), WorkItemID: workItem}, nil
}

// FactsPage is one page of the facts list, newest activity first.
// NextBefore is nil when no Work Item is left.
type FactsPage struct {
	Facts      []WorkItemFacts
	NextBefore *FactsCursor
}

const factsCandidateQuery = `WITH candidates AS (
		SELECT i.id, ` + factsActivity + ` AS at
		FROM work_items i
		WHERE ($2::text[] IS NULL OR i.team = ANY($2)) AND ($3::text = '' OR i.team = $3)
		  AND (cardinality($1::text[]) = 0 OR i.id IN (SELECT people.id FROM (` + factsPeople + `) people WHERE lower(people.login) = ANY($1)))
	)
	SELECT id, at FROM candidates
	WHERE ($4::timestamptz IS NULL OR at >= $4)
	  AND ($5::timestamptz IS NULL OR (at, id) < ($5, $6))
	ORDER BY at DESC, id DESC LIMIT $7`

// WorkItemFactsList lists the delivery facts of the Work Items in scope,
// newest activity first (ADR-0079). Each entry has the same shape as
// WorkItemFacts.
func (s *Store) WorkItemFactsList(ctx context.Context, f FactsListFilter, opts FactsOptions) (FactsPage, error) {
	page := FactsPage{Facts: []WorkItemFacts{}}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultFactsListLimit
	}
	limit = min(limit, FactsListLimit)
	logins := factsLogins(f.Members)
	var beforeAt *time.Time
	var beforeID int64
	if f.Before != nil {
		at := f.Before.At
		beforeAt, beforeID = &at, f.Before.WorkItemID
	}
	rows, err := s.pool.Query(ctx, factsCandidateQuery, logins, f.Teams, f.Team, f.Since, beforeAt, beforeID, limit+1)
	if err != nil {
		return FactsPage{}, err
	}
	type candidate struct {
		id int64
		at time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.at); err != nil {
			rows.Close()
			return FactsPage{}, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FactsPage{}, err
	}
	more := len(candidates) > limit
	if more {
		candidates = candidates[:limit]
	}
	for _, c := range candidates {
		facts, err := s.WorkItemFacts(ctx, c.id, f.Teams, opts)
		if errors.Is(err, ErrOperatorNotFound) {
			continue
		}
		if err != nil {
			return FactsPage{}, err
		}
		page.Facts = append(page.Facts, facts)
	}
	if more {
		last := candidates[len(candidates)-1]
		page.NextBefore = &FactsCursor{At: last.at.UTC(), WorkItemID: last.id}
	}
	return page, nil
}

func factsLogins(members []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range members {
		l := strings.ToLower(strings.TrimSpace(m))
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

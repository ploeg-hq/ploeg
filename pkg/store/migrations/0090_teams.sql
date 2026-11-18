CREATE SEQUENCE team_revisions;

CREATE TABLE teams (
    id              TEXT        PRIMARY KEY CHECK (id ~ '^team_[0-9a-hjkmnp-tv-z]{26}$'),
    tenant          TEXT,
    source          TEXT        NOT NULL CHECK (source IN ('static', 'runtime')),
    name            TEXT        CHECK (name IS NULL OR length(name) BETWEEN 1 AND 80),
    manifest        JSONB,
    revision        BIGINT      NOT NULL DEFAULT nextval('team_revisions'),
    state           TEXT        NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'shadowed', 'invalid')),
    shadowed_by     TEXT,
    invalid_reason  TEXT        CHECK (invalid_reason IN ('harness_not_offered', 'harness_not_qualified',
                        'model_not_allowed', 'target_unavailable', 'spend_exceeds_tenant', 'creation_disabled')),
    draining_since  TIMESTAMPTZ,
    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    removed_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at      TIMESTAMPTZ,
    CHECK ((source = 'static') = (tenant IS NULL)),
    CHECK (source = 'runtime' OR (manifest IS NULL AND state = 'active' AND draining_since IS NULL)),
    CHECK (source = 'static' OR manifest IS NOT NULL),
    CHECK (state <> 'invalid' OR invalid_reason IS NOT NULL),
    CHECK (state = 'invalid' OR invalid_reason IS NULL),
    CHECK ((state = 'shadowed') = (shadowed_by IS NOT NULL))
);

CREATE INDEX teams_live_by_tenant ON teams (tenant, source) WHERE removed_at IS NULL;

CREATE TABLE team_keys (
    team_id     TEXT        NOT NULL REFERENCES teams (id),
    tenant      TEXT,
    key         TEXT        NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
    kind        TEXT        NOT NULL CHECK (kind IN ('current', 'alias')),
    since       TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX team_keys_one_live_holder
    ON team_keys (coalesce(tenant, ''), lower(key)) WHERE released_at IS NULL;

CREATE UNIQUE INDEX team_keys_one_live_static_holder
    ON team_keys (lower(key)) WHERE tenant IS NULL AND released_at IS NULL;

CREATE UNIQUE INDEX team_keys_one_current_key
    ON team_keys (team_id) WHERE kind = 'current' AND released_at IS NULL;

CREATE INDEX team_keys_by_team ON team_keys (team_id);

CREATE INDEX team_keys_released ON team_keys (coalesce(tenant, ''), lower(key), released_at DESC)
    WHERE released_at IS NOT NULL;

ALTER TABLE work_items ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE shifts ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE agent_runs ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE leases ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE chats ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE work_item_premise_checks ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE tracker_bindings ADD COLUMN team_id TEXT REFERENCES teams (id);

CREATE INDEX work_items_by_team_id ON work_items (team_id, priority DESC, created_at);
CREATE INDEX shifts_live_by_team_id ON shifts (team_id) WHERE closed_at IS NULL;
CREATE INDEX agent_runs_pending_by_team_id ON agent_runs (team_id, role, id) WHERE state = 'pending';
CREATE INDEX agent_runs_claimable_harness_by_team_id ON agent_runs (team_id, role, harness_requested, id) WHERE state = 'pending';
CREATE INDEX leases_by_team_id ON leases (team_id);
CREATE INDEX tracker_bindings_live_by_team_id ON tracker_bindings (team_id) WHERE removed_at IS NULL;

CREATE OR REPLACE FUNCTION ploeg_static_team_id(team_key TEXT) RETURNS TEXT
LANGUAGE sql STABLE AS $$
    SELECT k.team_id FROM team_keys k JOIN teams t ON t.id = k.team_id
    WHERE k.tenant IS NULL AND k.released_at IS NULL AND t.removed_at IS NULL
      AND lower(k.key) = lower(team_key)
    LIMIT 1
$$;

CREATE OR REPLACE FUNCTION ploeg_set_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.team_id IS NULL THEN
            NEW.team_id := ploeg_static_team_id(NEW.team);
        END IF;
    ELSIF NEW.team IS DISTINCT FROM OLD.team AND NEW.team_id IS NOT DISTINCT FROM OLD.team_id THEN
        NEW.team_id := ploeg_static_team_id(NEW.team);
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER work_items_team_id BEFORE INSERT OR UPDATE OF team ON work_items
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER shifts_team_id BEFORE INSERT OR UPDATE OF team ON shifts
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER agent_runs_team_id BEFORE INSERT OR UPDATE OF team ON agent_runs
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER leases_team_id BEFORE INSERT OR UPDATE OF team ON leases
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER chats_team_id BEFORE INSERT OR UPDATE OF team ON chats
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER work_item_premise_checks_team_id BEFORE INSERT OR UPDATE OF team ON work_item_premise_checks
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();
CREATE TRIGGER tracker_bindings_team_id BEFORE INSERT OR UPDATE OF team ON tracker_bindings
    FOR EACH ROW EXECUTE FUNCTION ploeg_set_team_id();

CREATE OR REPLACE FUNCTION ploeg_audit_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    resolved TEXT;
BEGIN
    IF jsonb_typeof(NEW.detail) = 'object' AND jsonb_typeof(NEW.detail -> 'team') = 'string'
       AND NOT NEW.detail ? 'team_id' THEN
        resolved := ploeg_static_team_id(NEW.detail ->> 'team');
        IF resolved IS NOT NULL THEN
            NEW.detail := NEW.detail || jsonb_build_object('team_id', resolved);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER audit_log_team_id BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION ploeg_audit_team_id();

CREATE OR REPLACE FUNCTION ploeg_grant_observer() RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ploeg_observer') THEN
        RETURN false;
    END IF;

    GRANT SELECT ON shifts, pull_requests, pull_request_reviews, pull_request_events,
        pull_request_ci_runs, pull_request_deployments, pull_request_reverts,
        deployments, deployment_checks, status_transitions, inference_allowances,
        forge_ci_gates, run_transcript_totals
        TO ploeg_observer;

    GRANT SELECT (id, provider, external_id, revision, team, state, origin, priority,
        attempts, title, url, created_at, updated_at, next_eligible_at, infra_failures,
        external_scope, target_forge, target_owner, target_repo, target_base_branch,
        route_rule, operator_owned, source_work_item_id, source_branch, source_pr,
        source_run_id, root_work_item_id, depth, ready, created_kind, budget_usd,
        route_hint, tracker_created_at, estimate_seconds, tracker_checked_at,
        harness_requested, model_requested, dependencies_checked_at, acts_for, handed_by,
        review_requeues, acts_for_proof, acts_for_act, kind, team_id)
        ON work_items TO ploeg_observer;

    GRANT SELECT (id, work_item_id, team, started_at, finished_at, outcome, links,
        usage, failure_reason, shift_id, role, round, writes, state, expires_at,
        authorized, verdict, evidence_version, delivery_source, trace_alias,
        configured_model, harness, harness_profile, plan_hash, ploegd_version, shadow,
        harness_version, harness_recorded_at, harness_requested, model_selected,
        prompt_started_at, limit_prompt_seconds, limit_idle_seconds, board_claimed_at,
        board_briefed_at, carrier, yields, yield_requested_at, team_id)
        ON agent_runs TO ploeg_observer;

    GRANT SELECT (work_item_id, team, shift_id, expires_at, renewed_at, team_id)
        ON leases TO ploeg_observer;

    GRANT SELECT (alias, authorized, models, ttl_seconds, state, observed_spend,
        reconciled_spend, updated_at, settled_at, settled_spend, corrections_until,
        cost_known, gateway_team_id, mcp_access_groups)
        ON run_llm_accounts TO ploeg_observer;

    GRANT SELECT (id, previous_spend, spend, recorded_at)
        ON run_llm_adjustments TO ploeg_observer;

    GRANT SELECT (shift_id, reserved) ON run_budget_holds TO ploeg_observer;

    GRANT SELECT (id, work_item_id, phase, created_at, node_name, pod_uid)
        ON checkpoints TO ploeg_observer;

    GRANT SELECT (id, work_item_id, gate, status, actor, at, received_at)
        ON gate_transitions TO ploeg_observer;

    GRANT SELECT (id, work_item_id, provider, repo, pr, reviewer, received_at, shift_id,
        round, state, head_sha)
        ON work_item_reviews TO ploeg_observer;

    GRANT SELECT (id, at, actor, action, work_item_id) ON audit_log TO ploeg_observer;

    GRANT SELECT (id, tenant, port, kind, instance, method, state, invalid_reason,
        expires_at, hand_over_agent, revision, created_at, updated_at, reconnected_at,
        last_probe_at, removed_at)
        ON connections TO ploeg_observer;

    IF to_regclass('tracker_bindings') IS NOT NULL THEN
        EXECUTE 'GRANT SELECT (id, tenant, team, connection, scope_id, default_target,
            allow_targets, revision, state, invalid_reason, webhook_state, webhook_id,
            webhook_source, created_at, updated_at, removed_at, team_id)
            ON tracker_bindings TO ploeg_observer';
    END IF;

    GRANT SELECT (id, work_item_id, shift_id, round, consumer, actor, audit_actor,
        expected_shift_id, branch, requested_at, yielded_at, yielded_commit,
        work_in_progress, pushed, handed_back_at, handed_back_shift_id)
        ON work_item_take_overs TO ploeg_observer;

    GRANT SELECT (id, tenant, team, connection, scope_id, default_target,
        allow_targets, revision, state, invalid_reason, webhook_state, webhook_id,
        webhook_source, created_at, updated_at, removed_at, team_id)
        ON tracker_bindings TO ploeg_observer;

    GRANT SELECT (id, tenant, target_key, connection, forge, repository, repository_id,
        branch, revision, state, invalid_reason, created_at, updated_at, removed_at)
        ON work_targets TO ploeg_observer;

    GRANT SELECT (id, tenant, source, revision, state, shadowed_by, invalid_reason,
        draining_since, created_at, updated_at, removed_at)
        ON teams TO ploeg_observer;

    GRANT SELECT (team_id, tenant, key, kind, since, released_at)
        ON team_keys TO ploeg_observer;

    RETURN true;
END
$$;

SELECT ploeg_grant_observer();

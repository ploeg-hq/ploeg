CREATE SEQUENCE work_target_revisions;

CREATE TABLE work_targets (
    id              BIGSERIAL   PRIMARY KEY,
    tenant          TEXT        NOT NULL DEFAULT 'default',
    target_key      TEXT        NOT NULL CHECK (target_key ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$'),
    connection      TEXT        NOT NULL CHECK (length(connection) BETWEEN 1 AND 128),
    forge           TEXT        NOT NULL CHECK (length(forge) BETWEEN 1 AND 128),
    repository      TEXT        NOT NULL CHECK (length(repository) BETWEEN 3 AND 512),
    repository_id   TEXT        NOT NULL CHECK (length(repository_id) BETWEEN 1 AND 128),
    branch          TEXT        NOT NULL CHECK (length(branch) BETWEEN 1 AND 255),
    label           TEXT        NOT NULL DEFAULT '' CHECK (length(label) <= 200),
    revision        BIGINT      NOT NULL DEFAULT nextval('work_target_revisions'),
    state           TEXT        NOT NULL CHECK (state IN ('active', 'shadowed', 'invalid')),
    shadowed_by     JSONB,
    invalid_reason  TEXT        CHECK (invalid_reason IN ('grant_revoked', 'installation_suspended',
                        'connection_unusable', 'repository_gone', 'repository_moved')),
    readiness       JSONB       NOT NULL DEFAULT '{}',
    created_by      TEXT        NOT NULL,
    updated_by      TEXT        NOT NULL,
    removed_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at      TIMESTAMPTZ,
    CHECK (state <> 'invalid' OR invalid_reason IS NOT NULL),
    CHECK (state <> 'active' OR invalid_reason IS NULL),
    CHECK ((state = 'shadowed') = (shadowed_by IS NOT NULL))
);

CREATE UNIQUE INDEX work_targets_one_live_per_repository
    ON work_targets (tenant, forge, lower(repository))
    WHERE removed_at IS NULL;

CREATE UNIQUE INDEX work_targets_one_live_per_key
    ON work_targets (tenant, lower(target_key))
    WHERE removed_at IS NULL;

CREATE INDEX work_targets_by_connection
    ON work_targets (connection)
    WHERE removed_at IS NULL;

CREATE INDEX work_targets_by_repository
    ON work_targets (tenant, forge, lower(repository), id DESC);

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
        review_requeues, acts_for_proof, acts_for_act, kind)
        ON work_items TO ploeg_observer;

    GRANT SELECT (id, work_item_id, team, started_at, finished_at, outcome, links,
        usage, failure_reason, shift_id, role, round, writes, state, expires_at,
        authorized, verdict, evidence_version, delivery_source, trace_alias,
        configured_model, harness, harness_profile, plan_hash, ploegd_version, shadow,
        harness_version, harness_recorded_at, harness_requested, model_selected,
        prompt_started_at, limit_prompt_seconds, limit_idle_seconds, board_claimed_at,
        board_briefed_at, carrier, yields, yield_requested_at)
        ON agent_runs TO ploeg_observer;

    GRANT SELECT (work_item_id, team, shift_id, expires_at, renewed_at)
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
            webhook_source, created_at, updated_at, removed_at)
            ON tracker_bindings TO ploeg_observer';
    END IF;

    GRANT SELECT (id, work_item_id, shift_id, round, consumer, actor, audit_actor,
        expected_shift_id, branch, requested_at, yielded_at, yielded_commit,
        work_in_progress, pushed, handed_back_at, handed_back_shift_id)
        ON work_item_take_overs TO ploeg_observer;

    GRANT SELECT (id, tenant, team, connection, scope_id, default_target,
        allow_targets, revision, state, invalid_reason, webhook_state, webhook_id,
        webhook_source, created_at, updated_at, removed_at)
        ON tracker_bindings TO ploeg_observer;

    GRANT SELECT (id, tenant, target_key, connection, forge, repository, repository_id,
        branch, revision, state, invalid_reason, created_at, updated_at, removed_at)
        ON work_targets TO ploeg_observer;

    RETURN true;
END
$$;

SELECT ploeg_grant_observer();

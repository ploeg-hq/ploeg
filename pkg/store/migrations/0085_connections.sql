-- ADR-0195: tracker and forge connections made through the operator API,
-- the GitHub Apps Ploeg created from a manifest, and the flows that make
-- them. Rows hold secret-store references only (ADR-0184), never a value.

CREATE TABLE connection_apps (
    id               TEXT        NOT NULL,
    tenant           TEXT        NOT NULL DEFAULT 'default',
    port             TEXT        NOT NULL CHECK (port IN ('tracker', 'forge')),
    kind             TEXT        NOT NULL,
    instance         TEXT        NOT NULL,
    app_id           TEXT        NOT NULL,
    slug             TEXT        NOT NULL,
    client_id        TEXT        NOT NULL DEFAULT '',
    owner            JSONB       NOT NULL DEFAULT '{}',
    html_url         TEXT        NOT NULL DEFAULT '',
    secrets          JSONB       NOT NULL DEFAULT '{}',
    created_by       TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at       TIMESTAMPTZ,
    PRIMARY KEY (tenant, id)
);
CREATE UNIQUE INDEX connection_apps_live_app ON connection_apps (tenant, instance, app_id) WHERE removed_at IS NULL;

CREATE TABLE connections (
    id                 TEXT        NOT NULL CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$'),
    tenant             TEXT        NOT NULL DEFAULT 'default',
    port               TEXT        NOT NULL CHECK (port IN ('tracker', 'forge')),
    kind               TEXT        NOT NULL,
    instance           TEXT        NOT NULL,
    method             TEXT        NOT NULL CHECK (method IN ('oauth', 'github_app_installation', 'token')),
    account_id         TEXT        NOT NULL,
    account            JSONB       NOT NULL DEFAULT '{}',
    app                TEXT,
    installation_id    TEXT,
    state              TEXT        NOT NULL CHECK (state IN ('active', 'awaiting_consent', 'invalid', 'token_missing')),
    invalid_reason     TEXT,
    secrets            JSONB       NOT NULL DEFAULT '{}',
    expires_at         TIMESTAMPTZ,
    hand_over_agent    BOOLEAN     NOT NULL DEFAULT false,
    label              TEXT        NOT NULL DEFAULT '' CHECK (char_length(label) <= 200),
    revision           BIGINT      NOT NULL DEFAULT 1,
    created_by         TEXT        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         TEXT        NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    reconnected_by     TEXT,
    reconnected_at     TIMESTAMPTZ,
    last_probe         JSONB,
    last_probe_at      TIMESTAMPTZ,
    probe_claimed_at   TIMESTAMPTZ,
    removal            JSONB       NOT NULL DEFAULT '{}',
    removed_at         TIMESTAMPTZ,
    PRIMARY KEY (tenant, id)
);
CREATE UNIQUE INDEX connections_live_account ON connections (tenant, port, kind, instance, account_id) WHERE removed_at IS NULL;
CREATE INDEX connections_installation ON connections (installation_id) WHERE installation_id IS NOT NULL AND removed_at IS NULL;

CREATE TABLE connection_flows (
    id                 TEXT        PRIMARY KEY,
    state_hash         BYTEA       NOT NULL UNIQUE,
    tenant             TEXT        NOT NULL DEFAULT 'default',
    person             TEXT        NOT NULL,
    consumer           TEXT        NOT NULL,
    method             TEXT        NOT NULL CHECK (method IN ('oauth', 'github_app_manifest', 'github_app_installation', 'token')),
    request            JSONB       NOT NULL,
    verifier           JSONB,
    status             TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completing', 'completed', 'failed')),
    outcome            JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    completed_at       TIMESTAMPTZ
);
CREATE INDEX connection_flows_open ON connection_flows (consumer, person) WHERE status IN ('pending', 'completing');

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
        review_requeues, acts_for_proof, acts_for_act)
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

    GRANT SELECT (id, work_item_id, shift_id, round, consumer, actor, audit_actor,
        expected_shift_id, branch, requested_at, yielded_at, yielded_commit,
        work_in_progress, pushed, handed_back_at, handed_back_shift_id)
        ON work_item_take_overs TO ploeg_observer;

    GRANT SELECT (id, tenant, port, kind, instance, method, state, invalid_reason,
        expires_at, hand_over_agent, revision, created_at, updated_at, reconnected_at,
        last_probe_at, removed_at)
        ON connections TO ploeg_observer;

    RETURN true;
END
$$;

SELECT ploeg_grant_observer();

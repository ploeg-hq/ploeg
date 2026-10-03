CREATE TABLE operator_executions (
    id TEXT PRIMARY KEY,
    consumer TEXT NOT NULL,
    actor TEXT NOT NULL,
    session_id TEXT NOT NULL,
    work_item_id BIGINT NOT NULL UNIQUE REFERENCES work_items(id) ON DELETE CASCADE,
    shift_id BIGINT NOT NULL REFERENCES shifts(id) ON DELETE CASCADE,
    run_id BIGINT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    generation BIGINT NOT NULL DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'admitted',
    supervision TEXT NOT NULL DEFAULT 'human',
    demo BOOLEAN NOT NULL DEFAULT FALSE,
    stop_confirmed BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ NOT NULL,
    last_block_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(consumer, session_id),
    CHECK (state IN ('admitted','running','waiting_input','pause_requested','paused','cancel_requested','cancelled','completed','failed','interrupted')),
    CHECK (supervision IN ('human','background'))
);

CREATE TABLE operator_execution_events (
    execution_id TEXT NOT NULL REFERENCES operator_executions(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor TEXT NOT NULL,
    kind TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY(execution_id, revision)
);

CREATE TABLE operator_execution_commands (
    execution_id TEXT NOT NULL REFERENCES operator_executions(id) ON DELETE CASCADE,
    command_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    response JSONB NOT NULL,
    PRIMARY KEY(execution_id, command_id)
);

CREATE INDEX operator_executions_expiry ON operator_executions(last_block_attempt_at NULLS FIRST,expires_at,id) WHERE state IN ('running','waiting_input','pause_requested','cancel_requested','interrupted') AND NOT stop_confirmed;

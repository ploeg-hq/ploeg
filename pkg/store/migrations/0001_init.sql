CREATE TABLE work_items (
    id          BIGSERIAL PRIMARY KEY,
    provider    TEXT        NOT NULL,
    external_id TEXT        NOT NULL,
    revision    TEXT        NOT NULL DEFAULT '',
    team        TEXT        NOT NULL DEFAULT '',
    state       TEXT        NOT NULL,
    origin      TEXT        NOT NULL DEFAULT 'assignment',
    priority    INT         NOT NULL DEFAULT 0,
    attempts    INT         NOT NULL DEFAULT 0,
    title       TEXT        NOT NULL DEFAULT '',
    url         TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, external_id)
);

-- The one poll-query shape (backlog #24): claimable items per team,
-- priority-then-FIFO (R10).
CREATE INDEX work_items_claimable
    ON work_items (team, priority DESC, created_at)
    WHERE state = 'queued';

-- One live lease per item is the primary key (R1, backlog #22).
CREATE TABLE leases (
    work_item_id BIGINT      PRIMARY KEY REFERENCES work_items (id),
    team         TEXT        NOT NULL,
    run_token    TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    renewed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX leases_expiry ON leases (expires_at);

CREATE TABLE checkpoints (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id),
    phase        TEXT        NOT NULL,
    branch       TEXT        NOT NULL DEFAULT '',
    pr_url       TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_runs (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id),
    team         TEXT        NOT NULL,
    run_token    TEXT        NOT NULL UNIQUE,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    outcome      TEXT,
    summary      TEXT        NOT NULL DEFAULT '',
    stuck_reason TEXT        NOT NULL DEFAULT '',
    links        TEXT[]      NOT NULL DEFAULT '{}'
);

CREATE TABLE audit_log (
    id           BIGSERIAL   PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor        TEXT        NOT NULL,
    action       TEXT        NOT NULL,
    work_item_id BIGINT,
    detail       JSONB       NOT NULL DEFAULT '{}'
);

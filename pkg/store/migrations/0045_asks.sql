-- ADR-0082: an Ask is a read-only Run with the Role 'ask' and no Shift, no
-- Round and no Lease, authorized against a periodic allowance instead of a
-- Shift pool.
--
-- One row per purpose, scope and period. Admission locks the row, so it plays
-- the part the Shift row plays for a Shift pool (ADR-0012). scope_kind and
-- purpose carry no CHECK: a Client or Tenant scope, or another purpose, is a
-- new value, not a migration. The limit is fixed when the period's first Ask
-- creates the row.
CREATE TABLE inference_allowances (
    id           BIGSERIAL      PRIMARY KEY,
    purpose      TEXT           NOT NULL,
    scope_kind   TEXT           NOT NULL,
    scope_id     TEXT           NOT NULL,
    period_start TIMESTAMPTZ    NOT NULL,
    period_end   TIMESTAMPTZ    NOT NULL,
    limit_usd    NUMERIC(12, 4) NOT NULL CHECK (limit_usd >= 0),
    created_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    UNIQUE (purpose, scope_kind, scope_id, period_start),
    CHECK (period_end > period_start)
);

-- The Ask's own facts beside its agent_runs row. The question is kept only as
-- its SHA-256; the consumer keeps the text. (consumer, ask_id) is the
-- consumer's idempotency key.
CREATE TABLE asks (
    run_id          BIGINT      PRIMARY KEY REFERENCES agent_runs (id) ON DELETE CASCADE,
    consumer        TEXT        NOT NULL,
    ask_id          TEXT        NOT NULL,
    actor           TEXT        NOT NULL,
    asked_by        TEXT        NOT NULL DEFAULT '',
    work_item_id    BIGINT      NOT NULL REFERENCES work_items (id),
    allowance_id    BIGINT      NOT NULL REFERENCES inference_allowances (id),
    question_sha256 TEXT        NOT NULL CHECK (question_sha256 ~ '^[0-9a-f]{64}$'),
    fingerprint     TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    UNIQUE (consumer, ask_id)
);

CREATE INDEX asks_by_allowance ON asks (allowance_id);
CREATE INDEX asks_by_work_item ON asks (work_item_id);

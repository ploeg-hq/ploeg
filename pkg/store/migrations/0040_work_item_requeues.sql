-- ADR-0044: an operator restarts stopped work from a Round they choose. One
-- row per accepted requeue. shift_id is the Shift the requeue opened, NULL
-- for a team without a Shift plan. command_id is the caller's idempotency
-- key; a replay with the same key returns this row instead of requeueing
-- again. fingerprint is the hash of the command the key was first used with.
CREATE TABLE work_item_requeues (
    id                BIGSERIAL      PRIMARY KEY,
    work_item_id      BIGINT         NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    command_id        TEXT,
    fingerprint       TEXT           NOT NULL,
    actor             TEXT           NOT NULL,
    from_round        INT            NOT NULL CHECK (from_round >= 1),
    pool_usd          NUMERIC(12, 4) NOT NULL DEFAULT 0 CHECK (pool_usd >= 0),
    note              TEXT           NOT NULL DEFAULT '',
    previous_state    TEXT           NOT NULL,
    previous_shift_id BIGINT         REFERENCES shifts (id) ON DELETE SET NULL,
    shift_id          BIGINT         REFERENCES shifts (id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT now(),
    UNIQUE (work_item_id, command_id)
);

CREATE UNIQUE INDEX work_item_requeues_by_shift ON work_item_requeues (shift_id) WHERE shift_id IS NOT NULL;

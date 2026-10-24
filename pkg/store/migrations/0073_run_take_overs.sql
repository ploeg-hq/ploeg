-- ADR-0142: a person takes over a running Run and hands it back.
--
-- yields is what the worker said at its claim: its Harness can stop at a
-- safe point. yield_requested_at is when a person asked the Run to yield;
-- the lease renewal answers it, and a Run reports yielded only after it.
ALTER TABLE agent_runs
    ADD COLUMN yields             BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN yield_requested_at TIMESTAMPTZ;

-- One row per take-over. (consumer, command_id) is the consumer's
-- idempotency key. actor is the person who took it over, the only one who
-- hands it back; audit_actor is how the audit log names the request. The
-- yielded_* columns are what the yielded Run's worker read on the forge; a
-- take-over whose Run finished another way keeps them NULL. A take-over is
-- active until a requeue hands it back, while its Shift is open or closed as
-- taken_over.
CREATE TABLE work_item_take_overs (
    id                   BIGSERIAL PRIMARY KEY,
    work_item_id         BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    shift_id             BIGINT      NOT NULL REFERENCES shifts (id) ON DELETE CASCADE,
    round                INTEGER     NOT NULL,
    consumer             TEXT        NOT NULL CHECK (length(consumer) BETWEEN 1 AND 128),
    command_id           TEXT        NOT NULL CHECK (length(command_id) BETWEEN 1 AND 128),
    actor                TEXT        NOT NULL CHECK (length(actor) BETWEEN 1 AND 256),
    audit_actor          TEXT        NOT NULL CHECK (length(audit_actor) BETWEEN 1 AND 512),
    expected_shift_id    BIGINT,
    branch               TEXT        NOT NULL,
    requested_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    yielded_at           TIMESTAMPTZ,
    yielded_commit       TEXT CHECK (yielded_commit ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    work_in_progress     TEXT CHECK (work_in_progress ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    pushed               BOOLEAN,
    yield_detail         TEXT CHECK (length(yield_detail) <= 2000),
    handed_back_at       TIMESTAMPTZ,
    handed_back_shift_id BIGINT REFERENCES shifts (id) ON DELETE SET NULL,
    UNIQUE (consumer, command_id)
);

CREATE INDEX work_item_take_overs_open ON work_item_take_overs (work_item_id, id DESC) WHERE handed_back_at IS NULL;
CREATE INDEX work_item_take_overs_handed_back ON work_item_take_overs (handed_back_shift_id) WHERE handed_back_shift_id IS NOT NULL;

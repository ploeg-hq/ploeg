CREATE TABLE operator_notes (
    id                BIGSERIAL   PRIMARY KEY,
    work_item_id      BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    consumer          TEXT        NOT NULL,
    command_id        TEXT        NOT NULL,
    actor             TEXT        NOT NULL,
    text              TEXT        NOT NULL CHECK (length(text) BETWEEN 1 AND 4096),
    expected_shift_id BIGINT,
    shift_id          BIGINT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed_at       TIMESTAMPTZ,
    consumed_by_run   BIGINT      REFERENCES agent_runs (id) ON DELETE SET NULL,
    UNIQUE (consumer, command_id)
);

CREATE INDEX operator_notes_unconsumed ON operator_notes (work_item_id, id) WHERE consumed_at IS NULL;

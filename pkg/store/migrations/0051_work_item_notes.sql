-- ADR-0088 (proposed): every learning a Run keeps becomes a Work Item Note in
-- the Tenant's notes graph, written without review. The outcome transaction
-- stores one row per kept learning; a ploegd loop writes the rows to the graph
-- and marks them written, so a restart or a lost response never loses or
-- doubles a note. note_key is the graph key, the Run's trace alias and the
-- learning's position, so a replayed outcome adds nothing.
CREATE TABLE work_item_note_outbox (
    id                BIGSERIAL   PRIMARY KEY,
    note_key          TEXT        NOT NULL UNIQUE,
    run_id            BIGINT      NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    work_item_id      BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    root_work_item_id BIGINT      NOT NULL,
    repository        TEXT        NOT NULL,
    learning          JSONB       NOT NULL,
    suggested_scope   TEXT        NOT NULL CHECK (suggested_scope IN ('work-item', 'repository', 'client', 'tenant')),
    written_by        TEXT        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts          INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_until     TIMESTAMPTZ,
    last_error        TEXT        NOT NULL DEFAULT '',
    written_at        TIMESTAMPTZ,
    graph_commit      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX work_item_note_outbox_pending ON work_item_note_outbox (next_attempt_at) WHERE written_at IS NULL;

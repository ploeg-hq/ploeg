-- ADR-0051: every move of a Work Item into another delivery gate, as the
-- tracker reported it. status is the tracker status or bucket title that
-- mapped to the gate. actor is the tracker username that made the move, NULL
-- when the tracker did not say. reason is the bounce reason Ploeg found on the
-- ticket when it recorded a move back to an earlier gate, NULL when it found
-- none or the move was not a bounce. at is when the tracker says the move
-- happened, or when Ploeg received it. Rows are read in id order, the order
-- Ploeg recorded them in.
CREATE TABLE gate_transitions (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    gate         TEXT        NOT NULL CHECK (gate IN ('development', 'test', 'acceptance', 'done')),
    status       TEXT        NOT NULL,
    actor        TEXT,
    reason       TEXT        CHECK (reason IN ('defect', 'requirement', 'misunderstood', 'environment')),
    at           TIMESTAMPTZ NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX gate_transitions_by_work_item ON gate_transitions (work_item_id, id);

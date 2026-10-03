-- ADR-0057: every move of a Work Item into another tracker status or bucket,
-- mapped to a gate or not, so a Run card can show the time spent in each.
-- gate is the delivery gate the status mapped to when Ploeg recorded the
-- move, NULL when it mapped to none. at is when the tracker says the move
-- happened; observed is true when the tracker did not say, or said a time
-- before the previous move, and at is when Ploeg received it. No actor is
-- kept: time in a status describes the ticket and the team's process, not a
-- person. Rows are read in id order, the order Ploeg recorded them in.
CREATE TABLE status_transitions (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    status       TEXT        NOT NULL CHECK (length(status) BETWEEN 1 AND 256),
    gate         TEXT        CHECK (gate IN ('development', 'test', 'acceptance', 'done')),
    at           TIMESTAMPTZ NOT NULL,
    observed     BOOLEAN     NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX status_transitions_by_work_item ON status_transitions (work_item_id, id);

-- When the tracker says the ticket was created, NULL until Ploeg read it,
-- and the tracker's time estimate in seconds, NULL when the tracker keeps
-- none or none was set.
ALTER TABLE work_items ADD COLUMN tracker_created_at TIMESTAMPTZ;
ALTER TABLE work_items ADD COLUMN estimate_seconds BIGINT CHECK (estimate_seconds >= 0);

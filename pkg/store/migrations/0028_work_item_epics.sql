-- ADR-0053: a Work Item's parent on its tracker (an epic, or a parent task),
-- as Ploeg last read it. The epic need not be a Work Item itself; it is named
-- by the tracker's own id within provider. first_seen_at is when Ploeg first
-- saw the relation and decides whether it counts: only a relation seen no
-- later than the child's first Shift makes the child part of the set.
-- removed_at is set when the tracker stopped reporting the relation; a
-- relation reported again afterwards starts over with a new first_seen_at.
CREATE TABLE work_item_epics (
    work_item_id     BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    provider         TEXT        NOT NULL,
    epic_external_id TEXT        NOT NULL CHECK (epic_external_id <> ''),
    epic_title       TEXT        NOT NULL DEFAULT '',
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at       TIMESTAMPTZ,
    PRIMARY KEY (work_item_id, provider, epic_external_id)
);

CREATE INDEX work_item_epics_by_epic ON work_item_epics (provider, epic_external_id) WHERE removed_at IS NULL;

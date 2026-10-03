ALTER TABLE work_items DROP CONSTRAINT work_items_state_known;
ALTER TABLE work_items ADD CONSTRAINT work_items_state_known
    CHECK (state IN ('ingested', 'queued', 'leased', 'needs_human', 'awaiting_review', 'stale', 'done', 'withdrawn')) NOT VALID;

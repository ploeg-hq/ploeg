-- ADR-0031: a Run may create Work Items. A created Work Item names its source
-- Work Item (source_work_item_id, added by 0018) and Run, sits one level deeper
-- than its source, belongs to the tree of its root Work Item, and carries the
-- Shift budget it was allotted. proposed holds it until a person approves or
-- rejects it.
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_state_known;
ALTER TABLE work_items ADD CONSTRAINT work_items_state_known
    CHECK (state IN ('ingested', 'proposed', 'queued', 'leased', 'needs_human', 'awaiting_review', 'stale', 'done', 'withdrawn')) NOT VALID;

ALTER TABLE work_items ADD COLUMN source_run_id BIGINT REFERENCES agent_runs (id) ON DELETE SET NULL;
ALTER TABLE work_items ADD COLUMN root_work_item_id BIGINT REFERENCES work_items (id) ON DELETE SET NULL;
ALTER TABLE work_items ADD COLUMN depth INT NOT NULL DEFAULT 0;
ALTER TABLE work_items ADD COLUMN ready BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE work_items ADD COLUMN created_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN budget_usd NUMERIC(12, 4) NOT NULL DEFAULT 0;

CREATE INDEX work_items_by_root ON work_items (root_work_item_id) WHERE root_work_item_id IS NOT NULL;
CREATE INDEX work_items_created_open ON work_items (source_run_id)
    WHERE source_run_id IS NOT NULL AND state IN ('proposed', 'queued', 'leased');

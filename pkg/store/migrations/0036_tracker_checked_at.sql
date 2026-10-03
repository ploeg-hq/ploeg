-- VIK-1615: the sweep re-reads a stopped Work Item's tracker task to settle
-- the ones a person closed while the close webhook was missed.
-- tracker_checked_at is when that last happened, so each item is read at
-- most once per interval.
ALTER TABLE work_items ADD COLUMN tracker_checked_at TIMESTAMPTZ;

CREATE INDEX work_items_stopped_tracker_check ON work_items (tracker_checked_at NULLS FIRST, id)
    WHERE state IN ('needs_human', 'awaiting_review') AND NOT operator_owned;

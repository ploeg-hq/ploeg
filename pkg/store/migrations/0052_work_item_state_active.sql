-- ADR-0093: the Work Item state `leased` is renamed `active`. A Work Item in it
-- has a live Shift, and a Round of readers holds no Lease, so the old name
-- described the wrong thing.
--
-- During a rolling update the outgoing ploegd may still write `leased` after
-- this migration ran. The trigger rewrites such a write to `active`, so the
-- table never holds the retired value again and every reader of this release
-- sees one spelling. A BEFORE trigger runs ahead of the check, so the
-- constraint names only `active`. A later migration drops the trigger once no
-- running release writes `leased`.
CREATE FUNCTION work_items_retire_leased() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'leased' THEN
        NEW.state := 'active';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER work_items_retire_leased BEFORE INSERT OR UPDATE OF state ON work_items
    FOR EACH ROW EXECUTE FUNCTION work_items_retire_leased();

ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_state_known;
UPDATE work_items SET state = 'active' WHERE state = 'leased';
ALTER TABLE work_items ADD CONSTRAINT work_items_state_known
    CHECK (state IN ('ingested', 'proposed', 'queued', 'active', 'needs_human', 'awaiting_review', 'stale', 'done', 'withdrawn')) NOT VALID;

DROP INDEX IF EXISTS work_items_created_open;
CREATE INDEX work_items_created_open ON work_items (source_run_id)
    WHERE source_run_id IS NOT NULL AND state IN ('proposed', 'queued', 'active');

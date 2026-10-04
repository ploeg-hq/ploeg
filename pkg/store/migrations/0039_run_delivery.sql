-- VIK-1732 (ADR-0059): the worker's delivery record as ploegd admitted it.
-- delivery_source says where a Run's delivery facts came from:
--   worker   - the worker's record, bound to the Work Item's forge,
--              repository and the Run's branch;
--   mismatch - the worker's record named another forge, repository, branch
--              or pull request; it is stored as observed=unknown with the
--              mismatch as its reason;
--   legacy   - a report with no record, from an older worker; only links to
--              a pull request of the Work Item's repository are kept.
-- A Run stored before this migration keeps NULL in both columns. Nothing here
-- turns its links into a delivery record.
ALTER TABLE agent_runs ADD COLUMN delivery JSONB
    CHECK (delivery IS NULL OR jsonb_typeof(delivery) = 'object');
ALTER TABLE agent_runs ADD COLUMN delivery_source TEXT
    CHECK (delivery_source IS NULL OR delivery_source IN ('worker', 'mismatch', 'legacy'));

-- VIK-1733: the worker's structured record of the checks it ran on a writing
-- Run's checkout. NULL for a reading Run and for a Run whose worker reported
-- none; readers fall back to the summary and findings prose only then.
ALTER TABLE agent_runs ADD COLUMN verification JSONB
    CHECK (verification IS NULL OR jsonb_typeof(verification) = 'object');

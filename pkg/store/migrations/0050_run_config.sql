-- ADR-0091: what each Run was configured with when ploegd claimed or admitted
-- it. NULL is unknown: every Run recorded before this migration, and every
-- field its source did not supply. Nothing is backfilled.
ALTER TABLE agent_runs
    ADD COLUMN configured_model TEXT CHECK (length(configured_model) BETWEEN 1 AND 256),
    ADD COLUMN harness          TEXT CHECK (length(harness) BETWEEN 1 AND 64),
    ADD COLUMN harness_profile  TEXT CHECK (length(harness_profile) BETWEEN 1 AND 64),
    ADD COLUMN plan_hash        TEXT CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN ploegd_version   TEXT CHECK (length(ploegd_version) BETWEEN 1 AND 128);

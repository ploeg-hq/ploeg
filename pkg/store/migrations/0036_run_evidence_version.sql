-- VIK-1780: ploegd sets evidence_version to 1 when it stores a reported
-- outcome. From then on a Run's verification is only the worker's structured
-- record (agent_runs.verification); no prose can supply a result or a commit.
-- A row stored before this migration keeps NULL and its prose is shown as
-- unverified history. Nothing here infers provenance from existing text.
ALTER TABLE agent_runs ADD COLUMN evidence_version SMALLINT
    CHECK (evidence_version IS NULL OR evidence_version >= 1);

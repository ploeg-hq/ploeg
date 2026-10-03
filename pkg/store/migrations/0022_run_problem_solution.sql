-- ADR-0042: a writing Run reports the problem it addressed and the solution it
-- delivered. The person who reviews the pull request reads both in Vloer.
ALTER TABLE agent_runs ADD COLUMN problem TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN solution TEXT NOT NULL DEFAULT '';

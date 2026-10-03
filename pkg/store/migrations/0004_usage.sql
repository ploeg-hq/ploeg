-- Per-run usage/cost reported by the harness (harness.OutcomeReport.usage,
-- backlog #66). NULL = the harness reported no structured usage.
ALTER TABLE agent_runs ADD COLUMN usage JSONB;

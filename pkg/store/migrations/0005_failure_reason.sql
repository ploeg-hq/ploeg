-- Run forensics (VIK-597): failure reason taxonomy for agent_runs:
-- infra_node / infra_llm / agent_error / budget / lease_lost. Set by the
-- worker (resolveOutcome) or the sweeper (ExpireLeases). NULL = not a
-- failure or unclassified. Node/pod identity on checkpoints lives in 0006.
ALTER TABLE agent_runs ADD COLUMN failure_reason TEXT;

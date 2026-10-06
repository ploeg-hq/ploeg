-- ADR-0076: the trace alias is the published correlation key between Ploeg,
-- the gateway and the log store. Storing it lets a read-only role join on it
-- without being granted run_token, which is a Run credential.
ALTER TABLE agent_runs
    ADD COLUMN trace_alias TEXT GENERATED ALWAYS AS ('ploeg-' || left(run_token, 12)) STORED;

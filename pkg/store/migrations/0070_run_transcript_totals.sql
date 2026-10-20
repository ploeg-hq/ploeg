-- ADR-0090: a Run's transcript totals, kept so a batch checks the per-Run
-- limits without counting the Run's entries again. A statement trigger adds
-- every insert into run_transcript_entries, so the totals stay exact whichever
-- process writes, including a ploegd still running the previous release during
-- a rolling upgrade. Entries are never updated or deleted on their own; both
-- tables die with their Run.
CREATE TABLE run_transcript_totals (
    run_id    BIGINT  PRIMARY KEY REFERENCES agent_runs (id) ON DELETE CASCADE,
    entries   INTEGER NOT NULL,
    bytes     BIGINT  NOT NULL,
    truncated BOOLEAN NOT NULL
);

CREATE FUNCTION run_transcript_totals_add() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO run_transcript_totals AS t (run_id, entries, bytes, truncated)
    SELECT run_id, count(*), sum(bytes), bool_or(kind = 'truncated') FROM added GROUP BY run_id
    ON CONFLICT (run_id) DO UPDATE SET entries = t.entries + excluded.entries,
        bytes = t.bytes + excluded.bytes, truncated = t.truncated OR excluded.truncated;
    RETURN NULL;
END
$$;

CREATE TRIGGER run_transcript_totals_add AFTER INSERT ON run_transcript_entries
    REFERENCING NEW TABLE AS added FOR EACH STATEMENT EXECUTE FUNCTION run_transcript_totals_add();

INSERT INTO run_transcript_totals (run_id, entries, bytes, truncated)
SELECT run_id, count(*), sum(bytes), bool_or(kind = 'truncated') FROM run_transcript_entries GROUP BY run_id;

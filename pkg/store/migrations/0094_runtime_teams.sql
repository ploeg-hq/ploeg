ALTER TABLE tracker_bindings DROP CONSTRAINT tracker_bindings_invalid_reason_check;
ALTER TABLE tracker_bindings ADD CONSTRAINT tracker_bindings_invalid_reason_check
    CHECK (invalid_reason IN ('team_removed', 'target_unavailable', 'team_unavailable'));

CREATE OR REPLACE FUNCTION ploeg_team_id_for(team_key TEXT, row_tenant TEXT) RETURNS TEXT
LANGUAGE sql STABLE AS $$
    SELECT coalesce(ploeg_static_team_id(team_key, row_tenant), (
        SELECT min(k.team_id) FROM team_keys k JOIN teams t ON t.id = k.team_id
        WHERE k.tenant = coalesce(nullif(row_tenant, ''), 'default') AND k.released_at IS NULL
          AND t.source = 'runtime' AND t.removed_at IS NULL AND lower(k.key) = lower(team_key)
        HAVING count(*) = 1))
$$;

CREATE OR REPLACE FUNCTION ploeg_set_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    resolved TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.team_id IS NULL THEN
            NEW.team_id := ploeg_team_id_for(NEW.team, to_jsonb(NEW) ->> 'tenant');
        END IF;
    ELSIF NEW.team IS DISTINCT FROM OLD.team AND NEW.team_id IS NOT DISTINCT FROM OLD.team_id THEN
        resolved := ploeg_team_id_for(NEW.team, to_jsonb(NEW) ->> 'tenant');
        IF resolved IS NOT NULL
           OR OLD.team_id IS NULL
           OR NOT EXISTS (SELECT 1 FROM teams t WHERE t.id = OLD.team_id AND t.source = 'runtime') THEN
            NEW.team_id := resolved;
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE INDEX agent_runs_pool_claimable ON agent_runs (harness_requested, id)
    WHERE state = 'pending' AND team_id IS NOT NULL AND carrier IS NULL;

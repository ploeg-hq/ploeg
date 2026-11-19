ALTER TABLE teams ADD COLUMN config_digest TEXT;

CREATE TABLE team_id_backfills (
    table_name   TEXT        PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION ploeg_static_team_id(team_key TEXT, row_tenant TEXT) RETURNS TEXT
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN coalesce(row_tenant, '') = '' THEN ploeg_static_team_id(team_key) END
$$;

CREATE OR REPLACE FUNCTION ploeg_set_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    resolved TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.team_id IS NULL THEN
            NEW.team_id := ploeg_static_team_id(NEW.team, to_jsonb(NEW) ->> 'tenant');
        END IF;
    ELSIF NEW.team IS DISTINCT FROM OLD.team AND NEW.team_id IS NOT DISTINCT FROM OLD.team_id THEN
        resolved := ploeg_static_team_id(NEW.team, to_jsonb(NEW) ->> 'tenant');
        IF resolved IS NOT NULL
           OR OLD.team_id IS NULL
           OR NOT EXISTS (SELECT 1 FROM teams t WHERE t.id = OLD.team_id AND t.source = 'runtime') THEN
            NEW.team_id := resolved;
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION ploeg_audit_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    resolved TEXT;
BEGIN
    IF jsonb_typeof(NEW.detail) = 'object' AND jsonb_typeof(NEW.detail -> 'team') = 'string'
       AND NOT NEW.detail ? 'team_id' THEN
        resolved := ploeg_static_team_id(NEW.detail ->> 'team', NEW.detail ->> 'tenant');
        IF resolved IS NOT NULL THEN
            NEW.detail := NEW.detail || jsonb_build_object('team_id', resolved);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

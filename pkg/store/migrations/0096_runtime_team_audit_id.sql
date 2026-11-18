CREATE OR REPLACE FUNCTION ploeg_audit_team_id() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    resolved TEXT;
BEGIN
    IF jsonb_typeof(NEW.detail) = 'object' AND jsonb_typeof(NEW.detail -> 'team') = 'string'
       AND NOT NEW.detail ? 'team_id' THEN
        resolved := ploeg_team_id_for(NEW.detail ->> 'team', NEW.detail ->> 'tenant');
        IF resolved IS NOT NULL THEN
            NEW.detail := NEW.detail || jsonb_build_object('team_id', resolved);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

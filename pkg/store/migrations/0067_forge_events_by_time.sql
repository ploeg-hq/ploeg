-- The operator connections view asks when Ploeg last heard from each
-- repository's forge webhook.
CREATE INDEX audit_log_forge_events ON audit_log (at) WHERE action LIKE 'forge.%';

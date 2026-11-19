-- ADR-0203: a GitLab forge connection can be a group service account Ploeg
-- made from a group Owner's short-lived token. Its token lives only in the
-- secret store of ADR-0184; the row keeps the method and the reference.
ALTER TABLE connections DROP CONSTRAINT connections_method_check;
ALTER TABLE connections ADD CONSTRAINT connections_method_check
    CHECK (method IN ('oauth', 'github_app_installation', 'token', 'gitlab_service_account'));
ALTER TABLE connection_flows DROP CONSTRAINT connection_flows_method_check;
ALTER TABLE connection_flows ADD CONSTRAINT connection_flows_method_check
    CHECK (method IN ('oauth', 'github_app_manifest', 'github_app_installation', 'token', 'gitlab_service_account'));

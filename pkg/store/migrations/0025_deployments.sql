-- ADR-0047: a pipeline reports "commit sha is live in environment". One row
-- per (repository, environment, sha); a repeated report changes nothing.
-- forge is the forge dialect and owner and name are lowercased, so a row
-- matches pull_requests on lower(repo_owner) and lower(repo_name).
CREATE TABLE deployments (
    id          BIGSERIAL   PRIMARY KEY,
    forge       TEXT        NOT NULL,
    repo_owner  TEXT        NOT NULL,
    repo_name   TEXT        NOT NULL,
    environment TEXT        NOT NULL CHECK (environment ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    sha         TEXT        NOT NULL CHECK (sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    deployed_at TIMESTAMPTZ NOT NULL,
    url         TEXT,
    source      TEXT        CHECK (source IN ('ci', 'gitops', 'manual')),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (forge, repo_owner, repo_name, environment, sha)
);

CREATE INDEX deployments_by_repository ON deployments (forge, repo_owner, repo_name, environment, deployed_at);

-- The first deploy of each environment that carried a pull request's merge
-- commit. A later deploy never moves first_deployed_at forward.
CREATE TABLE pull_request_deployments (
    pull_request_id   BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    environment       TEXT        NOT NULL,
    deployment_id     BIGINT      NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    first_deployed_at TIMESTAMPTZ NOT NULL,
    marked_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, environment)
);

CREATE INDEX pull_requests_merged_by_repository ON pull_requests (forge, lower(repo_owner), lower(repo_name), merged_at DESC)
    WHERE state = 'merged' AND merge_commit_sha IS NOT NULL;

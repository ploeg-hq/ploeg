-- ADR-0047, VIK-1763: a deployment stays pending until every candidate pull
-- request was compared, and the sweep resumes it. deployment_checks keeps
-- each conclusive comparison; ancestry of two fixed commits never changes.
ALTER TABLE deployments
    ADD COLUMN checked_at     TIMESTAMPTZ,
    ADD COLUMN check_after    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN check_attempts INTEGER     NOT NULL DEFAULT 0;

CREATE INDEX deployments_pending_check ON deployments (check_after, id) WHERE checked_at IS NULL;

CREATE TABLE deployment_checks (
    deployment_id   BIGINT      NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    carried         BOOLEAN     NOT NULL,
    checked_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (deployment_id, pull_request_id)
);

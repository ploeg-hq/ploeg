CREATE TABLE credential_gateway_grants (
    run_token   TEXT        PRIMARY KEY REFERENCES agent_runs(run_token) ON DELETE CASCADE,
    namespace   TEXT        NOT NULL CHECK (length(namespace) BETWEEN 1 AND 63),
    pod_name    TEXT        NOT NULL CHECK (length(pod_name) BETWEEN 1 AND 253),
    pod_uid     TEXT        NOT NULL CHECK (length(pod_uid) BETWEEN 1 AND 64),
    pod_ip      TEXT        NOT NULL CHECK (length(pod_ip) BETWEEN 1 AND 64),
    sealed      BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE credential_gateway_use (
    run_token   TEXT        NOT NULL REFERENCES agent_runs(run_token) ON DELETE CASCADE,
    family      TEXT        NOT NULL CHECK (length(family) BETWEEN 1 AND 32),
    credential  TEXT        NOT NULL CHECK (length(credential) BETWEEN 1 AND 32),
    requests    BIGINT      NOT NULL DEFAULT 0,
    refused     BIGINT      NOT NULL DEFAULT 0,
    bytes       BIGINT      NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_token, family, credential)
);

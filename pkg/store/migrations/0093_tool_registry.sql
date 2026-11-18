CREATE SEQUENCE tool_registry_revisions;

CREATE TABLE mcp_servers (
    id          TEXT        PRIMARY KEY CHECK (id ~ '^mcp_[0-9a-z]{26}$'),
    tenant      TEXT        NOT NULL DEFAULT 'default',
    server_key  TEXT        NOT NULL CHECK (server_key ~ '^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$'),
    name        TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 320),
    url         TEXT        NOT NULL CHECK (length(url) BETWEEN 8 AND 2048),
    transport   TEXT        NOT NULL CHECK (transport = 'streamable-http'),
    auth        JSONB       NOT NULL,
    credential  JSONB,
    tools       JSONB       NOT NULL,
    adapter     TEXT        NOT NULL DEFAULT '',
    revision    BIGINT      NOT NULL DEFAULT nextval('tool_registry_revisions'),
    created_by  TEXT        NOT NULL,
    updated_by  TEXT        NOT NULL,
    removed_by  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at  TIMESTAMPTZ,
    CHECK (credential IS NULL OR (credential ? 'store' AND credential ? 'path' AND NOT credential ? 'value'))
);

CREATE UNIQUE INDEX mcp_servers_one_live_per_key
    ON mcp_servers (tenant, server_key)
    WHERE removed_at IS NULL;

CREATE TABLE skill_bundles (
    digest      TEXT        PRIMARY KEY CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    archive     BYTEA       NOT NULL CHECK (octet_length(archive) <= 60 * 1024 * 1024),
    skills      JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE skill_sources (
    id            TEXT        PRIMARY KEY CHECK (id ~ '^skl_[0-9a-z]{26}$'),
    tenant        TEXT        NOT NULL DEFAULT 'default',
    source_key    TEXT        NOT NULL CHECK (source_key ~ '^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$'),
    name          TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 320),
    kind          TEXT        NOT NULL CHECK (kind IN ('oci', 'https', 'git')),
    ref           TEXT        NOT NULL DEFAULT '' CHECK (length(ref) <= 2048),
    url           TEXT        NOT NULL DEFAULT '' CHECK (length(url) <= 2048),
    commit_id     TEXT        NOT NULL DEFAULT '' CHECK (commit_id = '' OR commit_id ~ '^[0-9a-f]{40}$'),
    digest        TEXT        NOT NULL DEFAULT '' CHECK (digest = '' OR digest ~ '^sha256:[0-9a-f]{64}$'),
    bundle_digest TEXT        NOT NULL REFERENCES skill_bundles (digest),
    skills        JSONB       NOT NULL,
    credential    JSONB,
    revision      BIGINT      NOT NULL DEFAULT nextval('tool_registry_revisions'),
    created_by    TEXT        NOT NULL,
    updated_by    TEXT        NOT NULL,
    removed_by    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at    TIMESTAMPTZ,
    CHECK (credential IS NULL OR (credential ? 'store' AND credential ? 'path' AND NOT credential ? 'value'))
);

CREATE UNIQUE INDEX skill_sources_one_live_per_key
    ON skill_sources (tenant, source_key)
    WHERE removed_at IS NULL;

CREATE TABLE team_tools (
    id          BIGSERIAL   PRIMARY KEY,
    tenant      TEXT        NOT NULL DEFAULT 'default',
    team        TEXT        NOT NULL CHECK (length(team) BETWEEN 1 AND 128),
    team_id     TEXT,
    roles       JSONB       NOT NULL,
    revision    BIGINT      NOT NULL DEFAULT nextval('tool_registry_revisions'),
    created_by  TEXT        NOT NULL,
    updated_by  TEXT        NOT NULL,
    removed_by  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX team_tools_one_live_per_team
    ON team_tools (tenant, coalesce(team_id, 'key:' || lower(team)))
    WHERE removed_at IS NULL;

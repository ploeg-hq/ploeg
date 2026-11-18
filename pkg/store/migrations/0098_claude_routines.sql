-- ADR-0197 section 7: a person's own Claude Code routine, registered once per
-- Team, that ploegd fires for a Work Item the person started. The routine's
-- trigger token lives only in the secret store of ADR-0184; this row keeps
-- its reference. No column holds a claude.ai credential or a trigger token.
CREATE TABLE claude_routines (
    person          TEXT        NOT NULL CHECK (length(person) BETWEEN 1 AND 256),
    team            TEXT        NOT NULL CHECK (length(team) BETWEEN 1 AND 128),
    fire_url        TEXT        NOT NULL CHECK (length(fire_url) BETWEEN 1 AND 512),
    secret_store    TEXT        NOT NULL CHECK (length(secret_store) BETWEEN 1 AND 64),
    secret_path     TEXT        NOT NULL CHECK (length(secret_path) BETWEEN 1 AND 512),
    secret_version  INTEGER     NOT NULL,
    creator         TEXT        NOT NULL DEFAULT '' CHECK (creator = '' OR creator ~ '^user:[A-Za-z0-9_-]{1,128}$'),
    registered_by   TEXT        NOT NULL,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (person, team)
);

-- One row per fire ploegd attempts, written before it calls Anthropic, so the
-- hourly counts hold across replicas and a session can be traced to the
-- person whose routine started it.
CREATE TABLE claude_routine_fires (
    id          BIGSERIAL   PRIMARY KEY,
    person      TEXT        NOT NULL,
    team        TEXT        NOT NULL,
    fire_url    TEXT        NOT NULL,
    run_id      BIGINT      REFERENCES agent_runs(id) ON DELETE SET NULL,
    session_id  TEXT        NOT NULL DEFAULT '',
    outcome     TEXT        NOT NULL DEFAULT 'pending' CHECK (outcome IN ('pending', 'fired', 'routine_token_refused',
                    'routine_not_found', 'routine_rate_limited', 'routines_unavailable')),
    fired_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX claude_routine_fires_recent ON claude_routine_fires (person, fired_at);
CREATE INDEX claude_routine_fires_trigger ON claude_routine_fires (fire_url, fired_at);
CREATE INDEX claude_routine_fires_session ON claude_routine_fires (session_id) WHERE session_id <> '';

-- A routine-carried Run is stored with its own carrier, so neither a worker
-- nor a client claim takes it and a push credential is never handed to the
-- runner for it.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_carrier_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_carrier_check CHECK (carrier IN ('client', 'routine'));

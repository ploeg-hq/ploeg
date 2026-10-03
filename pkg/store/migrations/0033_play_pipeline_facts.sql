-- ADR-0058: the pull request, CI and change-shape figures of a Run card's
-- plays. A NULL column is a fact the forge has not reported, never a zero.
-- opened_at is the forge's creation time (first_seen_at stays Ploeg's own
-- observation), author the login that opened the pull request and draft
-- whether it is a draft now.
ALTER TABLE pull_requests ADD COLUMN opened_at       TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN author          TEXT;
ALTER TABLE pull_requests ADD COLUMN draft           BOOLEAN;

-- Set by each forge activity read: the commits counted (a lower bound when
-- activity_truncated), the earliest author date among them, and the force
-- pushes, NULL on a forge that does not report them.
ALTER TABLE pull_requests ADD COLUMN commits              INT CHECK (commits >= 0);
ALTER TABLE pull_requests ADD COLUMN first_commit_at      TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN force_pushes         INT CHECK (force_pushes >= 0);
ALTER TABLE pull_requests ADD COLUMN activity_captured_at TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN activity_truncated   BOOLEAN;

-- Set by each CI read: where the runs came from and whether the read
-- reached its bound.
ALTER TABLE pull_requests ADD COLUMN ci_runs_captured_at TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN ci_runs_source      TEXT CHECK (ci_runs_source IN ('actions', 'statuses', 'pipelines'));
ALTER TABLE pull_requests ADD COLUMN ci_runs_truncated   BOOLEAN;

-- kpis holds the derived timeline and CI figures, recomputed from the rows
-- below and pull_request_reviews whenever they change, so a card read does
-- not derive them. shape holds the change-shape figures measured once at
-- the merge from the diff and the changed files; the diff itself is never
-- stored.
ALTER TABLE pull_requests ADD COLUMN kpis              JSONB CHECK (kpis IS NULL OR jsonb_typeof(kpis) = 'object');
ALTER TABLE pull_requests ADD COLUMN kpis_computed_at  TIMESTAMPTZ;
ALTER TABLE pull_requests ADD COLUMN shape             JSONB CHECK (shape IS NULL OR jsonb_typeof(shape) = 'object');

-- Who did what on the pull request's conversation, and when. No comment or
-- review text is kept. Replaced whole by each activity read.
CREATE TABLE pull_request_events (
    id              BIGSERIAL   PRIMARY KEY,
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('comment', 'review_comment', 'review', 'push', 'force_push', 'ready', 'draft')),
    actor           TEXT        NOT NULL DEFAULT '',
    at              TIMESTAMPTZ NOT NULL,
    state           TEXT        CHECK (state IN ('approved', 'changes_requested', 'commented')),
    head_sha        TEXT
);

CREATE INDEX pull_request_events_by_pull_request ON pull_request_events (pull_request_id, at);

-- One CI run (an Actions run, a pipeline, or the checks of one commit) of
-- the pull request. jobs is a JSON array of {name, status, startedAt,
-- completedAt, queuedSeconds, attempt}; job names are kept, logs never.
-- Replaced whole by each CI read.
CREATE TABLE pull_request_ci_runs (
    id              BIGSERIAL   PRIMARY KEY,
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    run_key         TEXT        NOT NULL CHECK (length(run_key) BETWEEN 1 AND 200),
    head_sha        TEXT        NOT NULL,
    workflow        TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL CHECK (status IN ('success', 'failure', 'error', 'cancelled', 'skipped', 'pending', 'running')),
    created_at      TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    jobs            JSONB       NOT NULL CHECK (jsonb_typeof(jobs) = 'array'),
    UNIQUE (pull_request_id, run_key)
);

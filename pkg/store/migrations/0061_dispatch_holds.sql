-- ADR-0123: what Ploeg checks before a Work Item's first Shift opens, and
-- what an active Shift says about where it works.
--
-- The items a Work Item waits for, as its tracker last reported them. A
-- dependency the tracker no longer reports is deleted: the tracker owns the
-- relation, Ploeg keeps only its newest reading. dependencies_checked_at is
-- when the tracker was last read; NULL is never.
CREATE TABLE work_item_dependencies (
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id) ON DELETE CASCADE,
    provider     TEXT        NOT NULL,
    external_id  TEXT        NOT NULL CHECK (length(external_id) BETWEEN 1 AND 256),
    title        TEXT        NOT NULL DEFAULT '',
    kind         TEXT        NOT NULL CHECK (kind IN ('blocked_by', 'follows')),
    done         BOOLEAN     NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (work_item_id, provider, external_id)
);

ALTER TABLE work_items ADD COLUMN dependencies_checked_at TIMESTAMPTZ;

-- One premise check per Work Item. verdict is NULL while attempts failed
-- without an answer; skipped means the check gave up and dispatch went on.
CREATE TABLE work_item_premise_checks (
    work_item_id BIGINT      PRIMARY KEY REFERENCES work_items (id) ON DELETE CASCADE,
    team         TEXT        NOT NULL,
    model        TEXT        NOT NULL,
    verdict      TEXT        CHECK (verdict IN ('proceed', 'ask', 'too_big', 'skipped')),
    question     TEXT        NOT NULL DEFAULT '',
    reason       TEXT        NOT NULL DEFAULT '',
    attempts     INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   TEXT        NOT NULL DEFAULT '',
    checked_at   TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((verdict IS NULL) = (checked_at IS NULL))
);

-- The paths a Shift's Runs said they plan to change, newest report wins.
-- The paths it did change come from its pull request (pull_request_paths).
ALTER TABLE shifts ADD COLUMN planned_paths TEXT[] NOT NULL DEFAULT '{}';

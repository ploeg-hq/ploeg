-- VIK-1698: the paths a pull request changes, as the forge listed them at
-- head_sha (ADR-0046). One capture per pull request: a newer head replaces
-- it. truncated is true when the pull request changed more paths than Ploeg
-- keeps. A pull request without a capture has unknown paths, which is not the
-- same as a capture with no paths.
CREATE TABLE pull_request_path_captures (
    pull_request_id BIGINT      NOT NULL REFERENCES pull_requests (id) ON DELETE CASCADE,
    head_sha        TEXT        NOT NULL CHECK (length(head_sha) BETWEEN 1 AND 128),
    truncated       BOOLEAN     NOT NULL,
    captured_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, head_sha)
);

CREATE TABLE pull_request_paths (
    pull_request_id BIGINT NOT NULL,
    head_sha        TEXT   NOT NULL,
    position        INT    NOT NULL CHECK (position >= 0),
    path            TEXT   NOT NULL CHECK (length(path) BETWEEN 1 AND 1024),
    status          TEXT   NOT NULL CHECK (status IN ('added', 'modified', 'deleted', 'renamed', 'copied')),
    previous_path   TEXT   CHECK (previous_path IS NULL OR length(previous_path) BETWEEN 1 AND 1024),
    PRIMARY KEY (pull_request_id, head_sha, position),
    FOREIGN KEY (pull_request_id, head_sha) REFERENCES pull_request_path_captures (pull_request_id, head_sha) ON DELETE CASCADE
);

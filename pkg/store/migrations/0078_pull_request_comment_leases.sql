CREATE TABLE pull_request_comment_leases (
    forge         TEXT        NOT NULL,
    repo          TEXT        NOT NULL,
    number        INT         NOT NULL CHECK (number > 0),
    key           TEXT        NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
    holder        TEXT        NOT NULL,
    claimed_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (forge, repo, number, key)
);

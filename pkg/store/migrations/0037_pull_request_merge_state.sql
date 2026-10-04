-- ADR-0040, VIK-1598: whether a pull request merges into its base branch, as
-- the review poll last confirmed it. A NULL merge_state is a pull request
-- never checked, and reads as unknown. unmergeable_polls counts the polls in
-- a row at merge_head_sha that reported a conflict; two confirm it.
ALTER TABLE pull_requests
    ADD COLUMN merge_state       TEXT CHECK (merge_state IN ('clean', 'conflicted', 'unknown')),
    ADD COLUMN merge_head_sha    TEXT,
    ADD COLUMN unmergeable_polls INT NOT NULL DEFAULT 0 CHECK (unmergeable_polls >= 0),
    ADD COLUMN base_branch       TEXT,
    ADD COLUMN merge_checked_at  TIMESTAMPTZ;

-- ADR-0046: the diff size and the CI result a Run card shows for each play.
-- A NULL column is a fact the forge has not reported yet, never a zero.
ALTER TABLE pull_requests ADD COLUMN additions     INT CHECK (additions >= 0);
ALTER TABLE pull_requests ADD COLUMN deletions     INT CHECK (deletions >= 0);
ALTER TABLE pull_requests ADD COLUMN changed_files INT CHECK (changed_files >= 0);

-- The combined commit status at ci_head_sha when it was read at
-- ci_captured_at. ci_checks is a JSON array of {context, state}.
ALTER TABLE pull_requests ADD COLUMN ci_state       TEXT CHECK (ci_state IN ('success', 'failure', 'pending', 'error'));
ALTER TABLE pull_requests ADD COLUMN ci_checks      JSONB CHECK (ci_checks IS NULL OR jsonb_typeof(ci_checks) = 'array');
ALTER TABLE pull_requests ADD COLUMN ci_head_sha    TEXT;
ALTER TABLE pull_requests ADD COLUMN ci_captured_at TIMESTAMPTZ;

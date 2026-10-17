-- ADR-0106: a dependency-update pull request someone else opened is explained
-- by a Team on the pull request. Ploeg records the title it explained, so a
-- pull request whose title changes (a dependency bot moved the pull request to
-- a newer version) is explained again, and one whose title is unchanged is
-- not. NULL is a pull request whose title Ploeg never recorded.
ALTER TABLE pull_requests ADD COLUMN title TEXT;

-- Node+pod identity on checkpoints for post-mortem forensics (VIK-597).
-- The downward-API env vars (NODE_NAME, POD_UID) are set in the chart.
ALTER TABLE checkpoints ADD COLUMN node_name TEXT NOT NULL DEFAULT '';
ALTER TABLE checkpoints ADD COLUMN pod_uid TEXT NOT NULL DEFAULT '';

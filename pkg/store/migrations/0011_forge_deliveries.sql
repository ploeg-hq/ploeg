-- Webhook delivery dedup (forge-ingest spec, backlog #4).
--
-- A forge retries a delivery it thinks failed, and a retry that acts twice is
-- how one review becomes two fix rounds. An in-memory set would forget across
-- exactly the restart a redelivery is most likely to follow, so this is a
-- table.
--
-- Rows are swept with the leases; the id is the forge's own delivery header,
-- which is opaque to us and compared only for equality.
CREATE TABLE forge_deliveries (
    provider    TEXT        NOT NULL,
    delivery_id TEXT        NOT NULL,
    seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, delivery_id)
);

-- The sweep walks by age.
CREATE INDEX forge_deliveries_by_age ON forge_deliveries (seen_at);

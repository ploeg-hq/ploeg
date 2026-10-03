-- Shifts: several personas work one Work Item, some of them at once.
-- ADR-0010 (Shift owns the item, Lease owns the branch), ADR-0012 (two-level
-- budgets), ADR-0013 (push rights minted per Run).
--
-- A Shift is one Team's engagement with one Work Item. It owns the branch, the
-- budget pool and the round counter. A Round is a set of Runs that start
-- together: either a fan-out of readers or a single writer, never both.
--
-- The load-bearing choice here is that a Round MATERIALISES its Runs. When a
-- Round opens, one agent_runs row per Role is inserted as 'pending'. Claiming
-- flips a pending row to 'running'. That buys three things at once:
--
--   * the roster is explicit and queryable rather than reconstructed;
--   * the KEDA scaler query and the claim predicate become the SAME query
--     ("pending runs for this team and role"), so they cannot drift — the
--     failure mode where undershoot stalls items silently and forever;
--   * reserved budget is a SUM over running rows, so it cannot disagree with
--     reality the way a hand-maintained counter can.

CREATE TABLE shifts (
    id           BIGSERIAL   PRIMARY KEY,
    work_item_id BIGINT      NOT NULL REFERENCES work_items (id),
    team         TEXT        NOT NULL,
    branch       TEXT        NOT NULL DEFAULT '',
    round        INT         NOT NULL DEFAULT 0,
    -- The pool for the whole item (ADR-0012). spent is settled cost; a Run's
    -- authorization lives on its agent_runs row, so `reserved` is derived and
    -- never drifts.
    budget       NUMERIC(12, 4) NOT NULL DEFAULT 0,
    spent        NUMERIC(12, 4) NOT NULL DEFAULT 0,
    opened_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at    TIMESTAMPTZ,
    close_reason TEXT        NOT NULL DEFAULT ''
);

-- Two Teams never hold a Shift on the same Work Item. Partial, so a closed
-- Shift does not block a later one (re-mandate after needs_human).
CREATE UNIQUE INDEX shifts_one_live_per_item
    ON shifts (work_item_id) WHERE closed_at IS NULL;

CREATE INDEX shifts_live ON shifts (team) WHERE closed_at IS NULL;

-- Runs gain their place in the Shift. Historical rows keep shift_id NULL:
-- they predate Shifts and must stay readable (R3 — no Run may vanish).
ALTER TABLE agent_runs ADD COLUMN shift_id   BIGINT REFERENCES shifts (id);
ALTER TABLE agent_runs ADD COLUMN role       TEXT   NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN round      INT    NOT NULL DEFAULT 0;
-- Writers take the Lease and mutate the tree; readers take none and may run
-- beside each other. This is the whole of the concurrency control (ADR-0010).
ALTER TABLE agent_runs ADD COLUMN writes     BOOLEAN NOT NULL DEFAULT TRUE;
-- pending -> running -> finished. The default is 'running' because that is
-- what an INSERT means on the pre-Shift Claim path: a row appears when a pod
-- starts. Historical rows are corrected by finished_at just below.
ALTER TABLE agent_runs ADD COLUMN state      TEXT   NOT NULL DEFAULT 'running';
-- Liveness is per-Run, not per-Lease. Readers hold no Lease (ADR-0010), so
-- lease expiry cannot be what detects a dead reader — every Run carries its
-- own deadline and renews it. NULL on historical rows, which are terminal.
ALTER TABLE agent_runs ADD COLUMN expires_at TIMESTAMPTZ;
-- The budget hold for this Run, released at settlement (ADR-0012). Summing
-- this over running rows IS the reserved figure; there is no counter to drift.
ALTER TABLE agent_runs ADD COLUMN authorized NUMERIC(12, 4) NOT NULL DEFAULT 0;

-- Correct the rows that predate this column: anything already reported is
-- finished, anything still open was running when the column landed.
UPDATE agent_runs SET state = 'finished' WHERE finished_at IS NOT NULL;

ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_state
    CHECK (state IN ('pending', 'running', 'finished'));

-- The Run sweeper: live Runs past their deadline, readers included. Partial so
-- it stays small however long agent_runs grows.
CREATE INDEX agent_runs_expiring
    ON agent_runs (expires_at) WHERE state = 'running';

-- A pending Run has not started yet. started_at has been NOT NULL since 0001,
-- which was correct when a row only came into existence once a pod was already
-- running. The DEFAULT stays, so the pre-Shift Claim path — which inserts
-- without naming the column — is untouched.
ALTER TABLE agent_runs ALTER COLUMN started_at DROP NOT NULL;

-- A finished Run has an outcome and a finish time; a pending one has neither.
-- Cheap to state here, and it stops a half-written settlement passing silently.
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_finished_shape
    CHECK (state <> 'finished' OR finished_at IS NOT NULL OR shift_id IS NULL);

-- THE claim predicate, and byte-for-byte the KEDA scaler query. Any divergence
-- between these two is the bug that stalls work invisibly, so they are one
-- index and one shape: pending runs for a team and role, oldest first.
CREATE INDEX agent_runs_claimable
    ON agent_runs (team, role, id) WHERE state = 'pending';

-- Settlement and the reserved sum both walk a Shift's runs.
CREATE INDEX agent_runs_by_shift ON agent_runs (shift_id, round);

-- Resume reads the latest checkpoint for an item (architecture §9.5); the
-- table has been written since 0001 and never read.
CREATE INDEX checkpoints_by_item ON checkpoints (work_item_id, created_at DESC);

-- The Lease is now a capability, not a note: the holder's push credential is
-- minted with it and revoked when it lapses (ADR-0013). Empty means no
-- credential was minted — tier 1, where readers simply receive a read-only
-- token and only writers get push rights.
ALTER TABLE leases ADD COLUMN forge_token_id TEXT NOT NULL DEFAULT '';
-- Which Shift's branch this Lease protects. work_item_id stays the primary
-- key: a live Shift is unique per Work Item, so one-writer-per-item and
-- one-writer-per-Shift are the same statement, and the existing R1 guarantee
-- carries over untouched.
ALTER TABLE leases ADD COLUMN shift_id BIGINT REFERENCES shifts (id);

CREATE TABLE run_llm_operator_settlements (
    consumer TEXT NOT NULL,
    command_id TEXT NOT NULL,
    run_token TEXT NOT NULL UNIQUE REFERENCES run_llm_accounts(run_token) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    previous_state TEXT NOT NULL CHECK (previous_state IN ('minting', 'issued', 'unknown', 'blocked')),
    spend NUMERIC(12, 4) NOT NULL CHECK (spend >= 0),
    released_hold NUMERIC(12, 4) NOT NULL CHECK (released_hold >= 0),
    evidence TEXT NOT NULL CHECK (evidence <> ''),
    settled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, command_id)
);

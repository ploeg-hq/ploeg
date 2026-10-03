-- ADR-0056: a Run card's rarity. pull_request_files gains the lines each
-- file added and removed, NULL when the forge did not say, so size can leave
-- lockfiles and generated files out.
ALTER TABLE pull_request_files ADD COLUMN additions INT CHECK (additions >= 0);
ALTER TABLE pull_request_files ADD COLUMN deletions INT CHECK (deletions >= 0);

-- One row per card the rarity sweep looked at or that was revealed.
-- checked_at is when the sweep last looked. Every other column is set once,
-- when the card is revealed, and never changes after: the tier is frozen
-- even when its cohort grows. score and predicted_score run from 0 to 100;
-- percentile is NULL when fixed thresholds decided the tier. inputs is the
-- cardRarityInputs object the score used.
CREATE TABLE card_rarity (
    work_item_id    BIGINT       PRIMARY KEY REFERENCES work_items (id) ON DELETE CASCADE,
    checked_at      TIMESTAMPTZ  NOT NULL,
    formula         TEXT         CHECK (length(formula) BETWEEN 1 AND 32),
    revealed_tier   TEXT         CHECK (revealed_tier IN ('common', 'uncommon', 'rare', 'epic', 'legendary')),
    predicted_tier  TEXT         CHECK (predicted_tier IN ('common', 'uncommon', 'rare', 'epic', 'legendary')),
    score           NUMERIC(4,1) CHECK (score BETWEEN 0 AND 100),
    predicted_score NUMERIC(4,1) CHECK (predicted_score BETWEEN 0 AND 100),
    percentile      NUMERIC(4,1) CHECK (percentile > 0 AND percentile <= 100),
    cohort_target   TEXT         CHECK (length(cohort_target) BETWEEN 3 AND 512),
    cohort_quarter  TEXT         CHECK (cohort_quarter ~ '^[0-9]{4}Q[1-4]$'),
    cohort_size     INT          CHECK (cohort_size >= 1),
    inputs          JSONB,
    revealed_at     TIMESTAMPTZ,
    recorded_at     TIMESTAMPTZ,
    CHECK (
        (revealed_tier IS NULL AND formula IS NULL AND predicted_tier IS NULL AND score IS NULL AND predicted_score IS NULL
            AND percentile IS NULL AND cohort_target IS NULL AND cohort_quarter IS NULL AND cohort_size IS NULL
            AND inputs IS NULL AND revealed_at IS NULL AND recorded_at IS NULL)
        OR
        (revealed_tier IS NOT NULL AND formula IS NOT NULL AND predicted_tier IS NOT NULL AND score IS NOT NULL
            AND predicted_score IS NOT NULL AND cohort_target IS NOT NULL AND cohort_quarter IS NOT NULL
            AND cohort_size IS NOT NULL AND inputs IS NOT NULL AND revealed_at IS NOT NULL AND recorded_at IS NOT NULL)
    )
);

CREATE INDEX card_rarity_cohort ON card_rarity (formula, cohort_target, cohort_quarter) WHERE revealed_tier IS NOT NULL;
CREATE INDEX card_rarity_unrevealed ON card_rarity (checked_at) WHERE revealed_tier IS NULL;

package store

import (
	"context"
	"regexp"
)

// RunConfig is what a Run was configured with when ploegd claimed or admitted
// it (ADR-0091). An empty field is unknown and is stored as NULL.
type RunConfig struct {
	ConfiguredModel string
	Harness         string
	HarnessProfile  string
	PlanHash        string
	PloegdVersion   string
}

var (
	runConfigName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	runConfigHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// RecordRunConfig stores a Run's configuration. Each field is written once:
// a field already recorded keeps its value, and an empty or malformed field
// stays unknown, so a value is never guessed or replaced.
func (s *Store) RecordRunConfig(ctx context.Context, runToken string, c RunConfig) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET
		configured_model = COALESCE(configured_model, NULLIF($2, '')),
		harness = COALESCE(harness, NULLIF($3, '')),
		harness_profile = COALESCE(harness_profile, NULLIF($4, '')),
		plan_hash = COALESCE(plan_hash, NULLIF($5, '')),
		ploegd_version = COALESCE(ploegd_version, NULLIF($6, ''))
		WHERE run_token = $1`, runToken,
		boundedText(c.ConfiguredModel, 256), matching(runConfigName, c.Harness), matching(runConfigName, c.HarnessProfile),
		matching(runConfigHash, c.PlanHash), boundedText(c.PloegdVersion, 128))
	return err
}

func boundedText(v string, max int) string {
	if len(v) > max {
		return ""
	}
	return v
}

func matching(re *regexp.Regexp, v string) string {
	if !re.MatchString(v) {
		return ""
	}
	return v
}

// OperatorRunConfig is a Run's recorded configuration as the operator API
// serves it (ADR-0091). A nil field is unknown.
type OperatorRunConfig struct {
	ConfiguredModel *string `json:"configuredModel"`
	Harness         *string `json:"harness"`
	HarnessProfile  *string `json:"harnessProfile"`
	PlanHash        *string `json:"planHash"`
	PloegdVersion   *string `json:"ploegdVersion"`
	// ModelMismatch is true when a gateway model group (alias) other than
	// ConfiguredModel served the Run, such as a fallback, and nil while
	// either side is unknown.
	ModelMismatch *bool `json:"modelMismatch"`
	// Shadow is true for a reading Run whose verdict and findings were
	// recorded and never acted (ADR-0094).
	Shadow bool `json:"shadow"`
}

const operatorRunConfigJSON = `'configuredModel', r.configured_model, 'harness', r.harness,
	'harnessProfile', r.harness_profile, 'planHash', r.plan_hash, 'ploegdVersion', r.ploegd_version,
	'modelMismatch', CASE WHEN r.configured_model IS NOT NULL AND jsonb_typeof(r.usage->'modelGroups') = 'array'
		AND jsonb_array_length(r.usage->'modelGroups') > 0
		THEN EXISTS (SELECT 1 FROM jsonb_array_elements(r.usage->'modelGroups') g WHERE g.value <> to_jsonb(r.configured_model)) END,
	'shadow', r.shadow`

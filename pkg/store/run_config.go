package store

import (
	"context"
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// RunConfig is what a Run was configured with when ploegd claimed or admitted
// it (ADR-0091). An empty field is unknown and is stored as NULL.
type RunConfig struct {
	ConfiguredModel string
	Harness         string
	HarnessProfile  string
	PlanHash        string
	PloegdVersion   string
	// HarnessVersion, AgentName and AgentVersion are what the worker said
	// about the Harness it ran (ADR-0097): the version its probe read, and
	// the agent's own name and version. An agent version without a name is
	// unknown.
	HarnessVersion string
	AgentName      string
	AgentVersion   string
}

var (
	runConfigName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	runConfigHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// RecordRunConfig stores a Run's configuration. Each field is written once:
// a field already recorded keeps its value, and an empty or malformed field
// stays unknown, so a value is never guessed or replaced.
func (s *Store) RecordRunConfig(ctx context.Context, runToken string, c RunConfig) error {
	_, err := s.RecordRunConfigVersion(ctx, runToken, c)
	return err
}

// RecordRunConfigVersion is RecordRunConfig that also returns the Harness
// version the Run holds after the write, empty when it is unknown or the Run
// does not exist, so a caller can tell when a reported version was kept out.
func (s *Store) RecordRunConfigVersion(ctx context.Context, runToken string, c RunConfig) (string, error) {
	agentName := printableText(c.AgentName, 128)
	agentVersion := ""
	if agentName != "" {
		agentVersion = printableText(c.AgentVersion, 128)
	}
	var recorded *string
	err := s.pool.QueryRow(ctx, `UPDATE agent_runs SET
		configured_model = COALESCE(configured_model, NULLIF($2, '')),
		harness = COALESCE(harness, NULLIF($3, '')),
		harness_profile = COALESCE(harness_profile, NULLIF($4, '')),
		plan_hash = COALESCE(plan_hash, NULLIF($5, '')),
		ploegd_version = COALESCE(ploegd_version, NULLIF($6, '')),
		harness_version = COALESCE(harness_version, NULLIF($7, '')),
		harness_recorded_at = COALESCE(harness_recorded_at, CASE WHEN harness_version IS NULL AND NULLIF($7, '') IS NOT NULL THEN now() END),
		harness_agent = COALESCE(harness_agent, CASE WHEN NULLIF($8::text, '') IS NOT NULL
			THEN jsonb_build_object('name', $8::text, 'version', NULLIF($9::text, '')) END)
		WHERE run_token = $1 RETURNING harness_version`, runToken,
		boundedText(c.ConfiguredModel, 256), matching(runConfigName, c.Harness), matching(runConfigName, c.HarnessProfile),
		matching(runConfigHash, c.PlanHash), boundedText(c.PloegdVersion, 128),
		printableText(c.HarnessVersion, 128), agentName, agentVersion).Scan(&recorded)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if recorded == nil {
		return "", nil
	}
	return *recorded, nil
}

func printableText(v string, max int) string {
	if v == "" || len(v) > max || !utf8.ValidString(v) {
		return ""
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return v
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
	// HarnessVersion and HarnessAgent are what the worker reported about the
	// Harness it ran (ADR-0097); nil is unknown.
	HarnessVersion *string               `json:"harnessVersion"`
	HarnessAgent   *OperatorHarnessAgent `json:"harnessAgent"`
	// ModelMismatch is true when a gateway model group (alias) other than
	// ConfiguredModel served the Run, such as a fallback, and nil while
	// either side is unknown.
	ModelMismatch *bool `json:"modelMismatch"`
	// Shadow is true for a reading Run whose verdict and findings were
	// recorded and never acted (ADR-0096).
	Shadow bool `json:"shadow"`
}

const operatorRunConfigJSON = `'configuredModel', r.configured_model, 'harness', r.harness,
	'harnessProfile', r.harness_profile, 'planHash', r.plan_hash, 'ploegdVersion', r.ploegd_version,
	'harnessVersion', r.harness_version, 'harnessAgent', r.harness_agent,
	'modelMismatch', CASE WHEN r.configured_model IS NOT NULL AND jsonb_typeof(r.usage->'modelGroups') = 'array'
		AND jsonb_array_length(r.usage->'modelGroups') > 0
		THEN EXISTS (SELECT 1 FROM jsonb_array_elements(r.usage->'modelGroups') g WHERE g.value <> to_jsonb(r.configured_model)) END,
	'shadow', r.shadow`

// OperatorHarnessAgent is the agent's own name and version as a Run recorded
// them (ADR-0097). A nil Version is unknown.
type OperatorHarnessAgent struct {
	Name    string  `json:"name"`
	Version *string `json:"version"`
}

package work

import (
	"regexp"
	"strings"
)

// DefaultReleaseEnvironment is the environment whose first deploy releases a
// merged change, for a Work Target that names no other (ADR-0047).
const DefaultReleaseEnvironment = "production"

var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// NormalizeEnvironment trims and lowercases a deploy environment name. ok is
// false when the result is not 1 to 63 lowercase letters, digits, dots,
// underscores and dashes starting with a letter or digit.
func NormalizeEnvironment(name string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized, environmentName.MatchString(normalized)
}

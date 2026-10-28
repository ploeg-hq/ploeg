package httpapi

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	maxRequiredClaims      = 10
	maxRequiredClaimValues = 20
)

func requiredPersonClaims(consumer string, configured map[string][]string, acceptsPersonTokens bool) (map[string][]string, error) {
	if len(configured) == 0 {
		return nil, nil
	}
	if !acceptsPersonTokens {
		return nil, fmt.Errorf("operator consumer %s names requireClaims but no clientIds or audiences, so it accepts no person token to check", consumer)
	}
	if len(configured) > maxRequiredClaims {
		return nil, fmt.Errorf("operator consumer %s requires more than %d claims", consumer, maxRequiredClaims)
	}
	required := make(map[string][]string, len(configured))
	for name, values := range configured {
		if !printableClaimText(name) {
			return nil, fmt.Errorf("operator consumer %s has an empty or non-printable claim name in requireClaims", consumer)
		}
		if len(values) == 0 || len(values) > maxRequiredClaimValues {
			return nil, fmt.Errorf("operator consumer %s requireClaims.%s needs 1 to %d values", consumer, name, maxRequiredClaimValues)
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !printableClaimText(v) || seen[v] {
				return nil, fmt.Errorf("operator consumer %s has an empty, duplicated or non-printable value in requireClaims.%s", consumer, name)
			}
			seen[v] = true
		}
		required[name] = slices.Clone(values)
	}
	return required, nil
}

func printableClaimText(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsFunc(s, func(r rune) bool { return r <= 0x20 || r >= 0x7f })
}

func (c *OperatorConsumer) unmetPersonClaim(claims map[string]any) string {
	names := make([]string, 0, len(c.requiredClaims))
	for name := range c.requiredClaims {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !claimHoldsOneOf(name, claims[name], c.requiredClaims[name]) {
			return name
		}
	}
	return ""
}

func claimHoldsOneOf(name string, value any, wanted []string) bool {
	switch v := value.(type) {
	case string:
		if name == "scope" {
			return slices.ContainsFunc(strings.Fields(v), func(s string) bool { return slices.Contains(wanted, s) })
		}
		return slices.Contains(wanted, v)
	case []any:
		return slices.ContainsFunc(v, func(e any) bool {
			s, ok := e.(string)
			return ok && slices.Contains(wanted, s)
		})
	}
	return false
}

// OperatorConsumersActingForAnyPerson names the consumers with execute that
// accept a person token as the bearer and require no claim of it: whoever
// the identity provider issues a token for their audience or client acts
// with their whole principal (threat model F-47).
func OperatorConsumersActingForAnyPerson(consumers []OperatorConsumer) []string {
	var names []string
	for _, c := range consumers {
		if c.Principal.CanExecute && len(c.clientIDs)+len(c.audiences) > 0 && len(c.requiredClaims) == 0 {
			names = append(names, c.Principal.Name)
		}
	}
	return names
}

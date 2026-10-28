package runidentity

import (
	"encoding/json"
	"fmt"
)

// Audiences is the allowlist ploegd signs Run identities for, by Team and
// then Role: Audiences["gold"]["builder"] names every audience a Run of
// Team gold in Role builder may ask for. A Team without a Shift plan runs
// in the Role "". A Team or Role it does not name gets no Run identity.
type Audiences map[string]map[string][]string

// ParseAudiences reads the allowlist from its JSON form
// ({"<team>": {"<role>": ["<audience>", ...]}}). Empty input is an empty
// allowlist. Every audience must be one ValidAudience accepts.
func ParseAudiences(raw string) (Audiences, error) {
	if raw == "" {
		return Audiences{}, nil
	}
	var a Audiences
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	for team, roles := range a {
		for role, auds := range roles {
			for _, aud := range auds {
				if why := ValidAudience(aud); why != "" {
					return nil, fmt.Errorf("team %q role %q: audience %q %s", team, role, aud, why)
				}
			}
		}
	}
	return a, nil
}

// Allows reports whether a Run of team in role may receive a Run identity
// for aud.
func (a Audiences) Allows(team, role, aud string) bool {
	for _, allowed := range a[team][role] {
		if allowed == aud {
			return true
		}
	}
	return false
}

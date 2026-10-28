package runidentity

import "testing"

func TestAudiencesAllowOnlyTheTeamAndRoleThatListThem(t *testing.T) {
	a, err := ParseAudiences(`{"gold":{"builder":["https://link.example","urn:example:a"],"":["urn:example:planless"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		team, role, aud string
		want            bool
	}{
		{"gold", "builder", "https://link.example", true},
		{"gold", "builder", "urn:example:a", true},
		{"gold", "", "urn:example:planless", true},
		{"gold", "reviewer", "https://link.example", false},
		{"silver", "builder", "https://link.example", false},
		{"gold", "builder", "https://other.example", false},
		{"gold", "", "https://link.example", false},
	} {
		if got := a.Allows(tc.team, tc.role, tc.aud); got != tc.want {
			t.Errorf("Allows(%q, %q, %q) = %v, want %v", tc.team, tc.role, tc.aud, got, tc.want)
		}
	}
}

func TestEmptyAudiencesAllowNothing(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		a, err := ParseAudiences(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if a.Allows("gold", "builder", "urn:example:a") {
			t.Errorf("%q allowed an audience it does not list", raw)
		}
	}
	var unset Audiences
	if unset.Allows("gold", "", "urn:example:a") {
		t.Error("a nil allowlist allowed an audience")
	}
}

func TestParseAudiencesRefusesMalformedAllowlists(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":   `{"gold":`,
		"plain http": `{"gold":{"builder":["http://link.example"]}}`,
		"query":      `{"gold":{"builder":["https://link.example/?k=v"]}}`,
		"flat list":  `["https://link.example"]`,
	} {
		if _, err := ParseAudiences(raw); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
}

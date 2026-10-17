package operatorclient

import "testing"

func TestPersonIDIsOIDCPrefixedSHA256OfIssuerAndSubject(t *testing.T) {
	const subject = "8f14e45fceea167a5a36dedd4bea2543"
	const want = "oidc-36ac1c14852c2a08866edca198991aba"
	for _, issuer := range []string{"https://id.example.com/application/o/ploeg/", "https://id.example.com/application/o/ploeg"} {
		if got := PersonID(issuer, subject); got != want {
			t.Fatalf("PersonID(%q) = %s, want %s", issuer, got, want)
		}
	}
	if got := PersonID("https://id.example.com/application/o/ploeg//", subject); got == want {
		t.Fatalf("only one trailing slash is removed, got %s", got)
	}
	if PersonID("https://id.example.com/application/o/ploeg", "another") == want {
		t.Fatal("another subject named the same person")
	}
	if !identity.MatchString(want) {
		t.Fatal("a person id must be a valid X-Ploeg-Actor")
	}
}

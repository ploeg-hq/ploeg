package cvegate

import (
	"strings"
	"testing"
)

func notAffected() VEXStatement {
	return VEXStatement{
		Vulnerability:   VEXVulnerability{Name: "CVE-2024-45492"},
		Products:        []VEXProduct{{ID: ProductID(testImage), Subcomponents: []VEXComponent{{ID: "pkg:deb/debian/libexpat1@2.5.0-1"}}}},
		Status:          "not_affected",
		Justification:   "vulnerable_code_not_in_execute_path",
		ImpactStatement: "Nothing in the image parses XML.",
	}
}

func document(statements ...VEXStatement) VEXDocument {
	return VEXDocument{Context: OpenVEXContext, ID: "https://example.invalid/vex", Author: "test", Timestamp: "2026-10-24T00:00:00Z", Version: 1, Statements: append([]VEXStatement{}, statements...)}
}

func TestAJustifiedStatementScopedToTheImageIsAccepted(t *testing.T) {
	if problems := document(notAffected()).Problems(testImage); len(problems) > 0 {
		t.Fatalf("refused: %v", problems)
	}
	unescaped := notAffected()
	unescaped.Products[0].ID = "pkg:oci/fixture?repository_url=ghcr.io/ploeg-hq/fixture"
	if problems := document(unescaped).Problems(testImage); len(problems) > 0 {
		t.Fatalf("refused the unescaped product purl: %v", problems)
	}
	if problems := document().Problems(testImage); len(problems) > 0 {
		t.Fatalf("refused an empty statements array: %v", problems)
	}
}

func TestStatementsThatWouldSuppressBlindlyAreRefused(t *testing.T) {
	cases := map[string]func(*VEXStatement){
		"no justification":               func(s *VEXStatement) { s.Justification = "" },
		"an invented justification":      func(s *VEXStatement) { s.Justification = "trust_me" },
		"no impact statement":            func(s *VEXStatement) { s.ImpactStatement = " " },
		"no product":                     func(s *VEXStatement) { s.Products = nil },
		"another image's product":        func(s *VEXStatement) { s.Products[0].ID = "pkg:oci/ploegd?repository_url=ghcr.io%2Fploeg-hq%2Fploegd" },
		"the whole image":                func(s *VEXStatement) { s.Products[0].Subcomponents = nil },
		"a subcomponent that is no purl": func(s *VEXStatement) { s.Products[0].Subcomponents[0].ID = "libexpat1" },
		"no vulnerability":               func(s *VEXStatement) { s.Vulnerability.Name = "" },
		"an unknown status":              func(s *VEXStatement) { s.Status = "ignored" },
		"affected without an action":     func(s *VEXStatement) { s.Status = "affected"; s.Justification = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			statement := notAffected()
			mutate(&statement)
			if problems := document(statement).Problems(testImage); len(problems) == 0 {
				t.Fatal("accepted")
			}
		})
	}
	missing := document()
	missing.Statements = nil
	if len(missing.Problems(testImage)) == 0 {
		t.Fatal("accepted a document without a statements array")
	}
	wrongContext := document()
	wrongContext.Context = "https://openvex.dev/ns/v0.0.1"
	if len(wrongContext.Problems(testImage)) == 0 {
		t.Fatal("accepted another OpenVEX version")
	}
}

func TestTheReleaseDocumentNamesThePublishedDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	release := document(notAffected()).ForRelease(testImage, digest, "https://example.invalid/release.openvex.json", "2026-10-25T00:00:00Z")
	if release.ID != "https://example.invalid/release.openvex.json" || release.Timestamp != "2026-10-25T00:00:00Z" {
		t.Fatalf("release document %s at %s", release.ID, release.Timestamp)
	}
	product := release.Statements[0].Products[0]
	want := "pkg:oci/fixture@sha256%3A" + strings.Repeat("b", 64) + "?repository_url=ghcr.io%2Fploeg-hq%2Ffixture"
	if product.ID != want {
		t.Fatalf("product %s, want %s", product.ID, want)
	}
	if product.Subcomponents[0].ID != "pkg:deb/debian/libexpat1@2.5.0-1" {
		t.Fatalf("subcomponents changed: %+v", product.Subcomponents)
	}
	if document(notAffected()).Statements[0].Products[0].ID != ProductID(testImage) {
		t.Fatal("rendering the release changed the source document")
	}
}

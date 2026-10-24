package cvegate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testImage = Image{Name: "fixture", Repository: "ghcr.io/ploeg-hq/fixture", Dockerfile: "Dockerfile"}

func fakeGrype(t *testing.T, report string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	body := filepath.Join(dir, "report.json")
	if err := os.WriteFile(body, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "grype")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + filepath.Join(dir, "args") + "\nenv > " + filepath.Join(dir, "env") + "\ncat " + body + "\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func emptyVEX(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeVEX(t, dir, VEXDocument{Context: OpenVEXContext, ID: "https://example.invalid/vex", Author: "test", Timestamp: "2026-10-24T00:00:00Z", Version: 1, Statements: []VEXStatement{}})
	return dir
}

func writeVEX(t *testing.T, dir string, document VEXDocument) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VEXPath(dir, testImage.Name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func request(t *testing.T, grype, vexDir string, budget Budget) Request {
	return Request{
		Grype:     grype,
		ImageRef:  "docker:ghcr.io/ploeg-hq/fixture:test",
		ImageName: testImage.Name,
		Budgets:   Budgets{Images: map[string]Budget{testImage.Name: budget}},
		Catalogue: Catalogue{Images: []Image{testImage}},
		VEXDir:    vexDir,
		OutDir:    t.TempDir(),
	}
}

const reportWithFindings = `{"source":{"type":"image"},"matches":[
 {"vulnerability":{"id":"CVE-1","severity":"Critical"}},
 {"vulnerability":{"id":"CVE-2","severity":"High"}},
 {"vulnerability":{"id":"CVE-3","severity":"High"}},
 {"vulnerability":{"id":"CVE-4","severity":"Medium"}}],
 "ignoredMatches":[{"appliedIgnoreRules":[{"namespace":"vex"}]},{"appliedIgnoreRules":[{"namespace":""}]}]}`

const cleanReport = `{"source":{"type":"image"},"matches":[],"ignoredMatches":[]}`

var zero = Budget{Mode: Enforce}

func TestFindingsOverAnEnforcedBudgetFail(t *testing.T) {
	verdict, err := Run(context.Background(), request(t, fakeGrype(t, reportWithFindings, 0), emptyVEX(t), zero))
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Critical != 1 || verdict.High != 2 || verdict.Suppressed != 1 {
		t.Fatalf("counted critical=%d high=%d suppressed=%d, want 1, 2 and 1", verdict.Critical, verdict.High, verdict.Suppressed)
	}
	if verdict.Exit() != OverBudget {
		t.Fatalf("exit %d, want %d", verdict.Exit(), OverBudget)
	}
}

func TestFindingsWithinTheBudgetPass(t *testing.T) {
	verdict, err := Run(context.Background(), request(t, fakeGrype(t, reportWithFindings, 0), emptyVEX(t), Budget{Mode: Enforce, Critical: 1, High: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Exit() != Pass {
		t.Fatalf("exit %d, want pass", verdict.Exit())
	}
}

func TestWarnModeReportsWithoutFailing(t *testing.T) {
	verdict, err := Run(context.Background(), request(t, fakeGrype(t, reportWithFindings, 0), emptyVEX(t), Budget{Mode: Warn}))
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Over() || verdict.Exit() != Pass {
		t.Fatalf("over=%t exit=%d, want over and pass", verdict.Over(), verdict.Exit())
	}
}

func TestACleanScanPasses(t *testing.T) {
	r := request(t, fakeGrype(t, cleanReport, 0), emptyVEX(t), zero)
	verdict, err := Run(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Exit() != Pass {
		t.Fatalf("exit %d, want pass", verdict.Exit())
	}
	if _, err := os.Stat(filepath.Join(r.OutDir, "grype.json")); err != nil {
		t.Fatalf("the raw report was not kept: %v", err)
	}
}

func TestTheScanRunsWithTheVEXFileAndThePinnedDatabasePolicy(t *testing.T) {
	grype := fakeGrype(t, cleanReport, 0)
	r := request(t, grype, emptyVEX(t), zero)
	if _, err := Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(grype), "args"))
	if !strings.Contains(string(args), "--vex "+VEXPath(r.VEXDir, testImage.Name)) || !strings.Contains(string(args), r.ImageRef) {
		t.Fatalf("grype ran with %q", args)
	}
	environment, _ := os.ReadFile(filepath.Join(filepath.Dir(grype), "env"))
	for _, setting := range GrypeDatabasePolicy {
		if !strings.Contains(string(environment), setting+"\n") {
			t.Errorf("grype ran without %s", setting)
		}
	}
}

func TestNoVerdictWithoutAScan(t *testing.T) {
	cases := map[string]Request{
		"grype fails":             request(t, fakeGrype(t, "", 1), emptyVEX(t), zero),
		"grype is missing":        request(t, filepath.Join(t.TempDir(), "grype"), emptyVEX(t), zero),
		"output is not a report":  request(t, fakeGrype(t, "database is too old", 0), emptyVEX(t), zero),
		"report names no source":  request(t, fakeGrype(t, `{"matches":[]}`, 0), emptyVEX(t), zero),
		"the VEX file is missing": request(t, fakeGrype(t, cleanReport, 0), t.TempDir(), zero),
	}
	unlisted := request(t, fakeGrype(t, cleanReport, 0), emptyVEX(t), zero)
	unlisted.ImageName = "unlisted"
	cases["the image is not in the catalogue"] = unlisted
	unpinned := request(t, fakeGrype(t, cleanReport, 0), emptyVEX(t), zero)
	unpinned.ImageRef = "ghcr.io/ploeg-hq/fixture:latest"
	cases["the reference is a mutable tag"] = unpinned
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Run(context.Background(), r); err == nil {
				t.Fatal("reached a verdict")
			}
		})
	}
}

func TestReferencesAcceptedForAScan(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for ref, want := range map[string]bool{
		"ghcr.io/ploeg-hq/ploegd" + digest:              true,
		"ghcr.io/ploeg-hq/ploegd:0.2.0" + digest:        true,
		"registry:localhost:5000/ploeg/ploegd" + digest: true,
		"docker:ghcr.io/ploeg-hq/ploegd:ci":             true,
		"oci-archive:dist/ploegd.tar":                   true,
		"ghcr.io/ploeg-hq/ploegd:0.2.0":                 false,
		"ghcr.io/ploeg-hq/ploegd@sha256:abc":            false,
		"dir:/":                                         false,
	} {
		if got := ValidateReference(ref) == nil; got != want {
			t.Errorf("ValidateReference(%q) accepted=%t, want %t", ref, got, want)
		}
	}
}

func TestBudgetsFallBackToTheDefaultsAndRefuseNonsense(t *testing.T) {
	budgets := Budgets{Defaults: Budget{Mode: Enforce}, Images: map[string]Budget{"named": {Mode: Warn, High: 3}}}
	if b, err := budgets.For("named"); err != nil || b.High != 3 {
		t.Fatalf("named budget %+v, %v", b, err)
	}
	if b, err := budgets.For("other"); err != nil || b.Mode != Enforce {
		t.Fatalf("default budget %+v, %v", b, err)
	}
	if _, err := (Budgets{}).For("other"); err == nil {
		t.Fatal("an image without a budget or usable defaults got one")
	}
	if (Budget{Mode: "audit"}).validate() == nil || (Budget{Mode: Enforce, High: -1}).validate() == nil {
		t.Fatal("an unknown mode or a negative budget was accepted")
	}
}

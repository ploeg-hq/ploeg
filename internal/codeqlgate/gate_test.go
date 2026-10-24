package codeqlgate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type result struct {
	rule string
	file string
	line int
}

func writeSARIF(t *testing.T, severities map[string]string, results []result, suppressed ...result) string {
	t.Helper()
	var rules []map[string]any
	for id, severity := range severities {
		rules = append(rules, map[string]any{"id": id, "properties": map[string]any{"security-severity": severity}})
	}
	entry := func(r result, suppressions []any) map[string]any {
		return map[string]any{
			"ruleId":       r.rule,
			"message":      map[string]any{"text": r.rule + " here"},
			"suppressions": suppressions,
			"locations": []any{map[string]any{"physicalLocation": map[string]any{
				"artifactLocation": map[string]any{"uri": r.file},
				"region":           map[string]any{"startLine": r.line},
			}}},
		}
	}
	var out []any
	for _, r := range results {
		out = append(out, entry(r, nil))
	}
	for _, r := range suppressed {
		out = append(out, entry(r, []any{map[string]any{"kind": "inSource"}}))
	}
	log := map[string]any{"version": "2.1.0", "runs": []any{map[string]any{
		"tool":    map[string]any{"driver": map[string]any{"name": "CodeQL"}, "extensions": []any{map[string]any{"name": "codeql/go-queries", "rules": rules}}},
		"results": out,
	}}}
	raw, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "go.sarif")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAccepted(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codeql-accepted.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func judge(t *testing.T, sarif string, accepted string, scope Scope) Verdict {
	t.Helper()
	findings, err := ReadFindings([]string{sarif})
	if err != nil {
		t.Fatal(err)
	}
	acceptances, err := LoadAcceptances(writeAccepted(t, accepted))
	if err != nil {
		t.Fatal(err)
	}
	return Judge(findings, acceptances, scope)
}

var severities = map[string]string{"go/sql-injection": "8.8", "go/log-injection": "7.8", "go/weak-crypto": "5.0", "go/hashing": "7.5"}

const hashingAccepted = `accepted:
  - rule: go/hashing
    file: pkg/a.go
    dismissal: false positive
    reason: The input is a random key, not a password.
`

func TestAHighFindingBlocks(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, []result{{"go/sql-injection", "pkg/db.go", 12}}), "accepted: []\n", Complete)
	if v.Exit() != Blocked || len(v.Blocking) != 1 || v.Blocking[0].Line != 12 {
		t.Fatalf("verdict %+v, want one blocking finding at line 12", v)
	}
}

func TestFindingsBelowHighPass(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, []result{{"go/weak-crypto", "pkg/a.go", 1}, {"go/unscored", "pkg/a.go", 2}}), "accepted: []\n", Complete)
	if v.Exit() != Pass {
		t.Fatalf("verdict %+v, want pass", v)
	}
}

func TestASuppressedResultPasses(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, nil, result{"go/sql-injection", "pkg/db.go", 12}), "accepted: []\n", Complete)
	if v.Exit() != Pass {
		t.Fatalf("verdict %+v, want pass", v)
	}
}

func TestAnAcceptedFindingPassesOnlyInItsFile(t *testing.T) {
	sarif := writeSARIF(t, severities, []result{{"go/hashing", "pkg/a.go", 3}, {"go/hashing", "pkg/a.go", 9}, {"go/hashing", "pkg/b.go", 4}})
	v := judge(t, sarif, hashingAccepted, Complete)
	if len(v.Accepted) != 2 || len(v.Blocking) != 1 || v.Blocking[0].File != "pkg/b.go" || v.Exit() != Blocked {
		t.Fatalf("verdict %+v, want pkg/a.go accepted twice and pkg/b.go blocking", v)
	}
}

func TestAnAcceptanceForAnotherRuleDoesNotCover(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, []result{{"go/log-injection", "pkg/a.go", 3}, {"go/hashing", "pkg/a.go", 4}}), hashingAccepted, Complete)
	if len(v.Blocking) != 1 || v.Blocking[0].Rule != "go/log-injection" {
		t.Fatalf("verdict %+v, want go/log-injection blocking", v)
	}
}

func TestAStaleAcceptanceBlocksACompleteRun(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, nil), hashingAccepted, Complete)
	if v.Exit() != Blocked || len(v.Stale) != 1 {
		t.Fatalf("verdict %+v, want the unused acceptance to block", v)
	}
}

func TestADiffInformedRunIgnoresUnusedAcceptances(t *testing.T) {
	v := judge(t, writeSARIF(t, severities, nil), hashingAccepted, DiffInformed)
	if v.Exit() != Pass {
		t.Fatalf("verdict %+v, want pass on a pull request that does not touch the accepted file", v)
	}
}

func TestAcceptancesAreRefusedWithoutAJustification(t *testing.T) {
	for name, body := range map[string]string{
		"no reason":         "accepted:\n  - rule: go/hashing\n    file: pkg/a.go\n    dismissal: false positive\n",
		"blank reason":      "accepted:\n  - rule: go/hashing\n    file: pkg/a.go\n    dismissal: false positive\n    reason: '  '\n",
		"no file":           "accepted:\n  - rule: go/hashing\n    dismissal: false positive\n    reason: why\n",
		"unknown dismissal": "accepted:\n  - rule: go/hashing\n    file: pkg/a.go\n    dismissal: fine\n    reason: why\n",
		"unknown field":     "accepted:\n  - rule: go/hashing\n    file: pkg/a.go\n    dismissal: false positive\n    reason: why\n    line: 3\n",
		"listed twice":      hashingAccepted + "  - rule: go/hashing\n    file: pkg/a.go\n    dismissal: won't fix\n    reason: again\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadAcceptances(writeAccepted(t, body)); err == nil {
				t.Fatal("accepted, want refused")
			}
		})
	}
}

func TestUnreadableSARIFHasNoVerdict(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.sarif")
	if err := os.WriteFile(empty, []byte(`{"runs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(t.TempDir(), "broken.sarif")
	if err := os.WriteFile(broken, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][]string{"none": nil, "no run": {empty}, "not json": {broken}, "missing": {filepath.Join(t.TempDir(), "absent.sarif")}} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadFindings(paths); err == nil {
				t.Fatal("read, want an error")
			}
		})
	}
}

func TestTheRepositoryAcceptancesLoad(t *testing.T) {
	if _, err := LoadAcceptances(filepath.Join("..", "..", "ops", "security", "codeql-accepted.yaml")); err != nil {
		t.Fatal(err)
	}
}

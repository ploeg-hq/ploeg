package rarity

import (
	"strings"
	"testing"
)

func TestPatternMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		{"**/migrations/**", []string{"migrations/0001.sql", "apps/ploeg/pkg/store/migrations/0031_card_rarity.sql"}, []string{"pkg/migration.go", "docs/migrations.md"}},
		{"migrations/**", []string{"migrations/0001.sql"}, []string{"apps/ploeg/migrations/0001.sql"}},
		{"**/*.sql", []string{"a.sql", "db/seed/b.sql"}, []string{"a.sqlx", "sql/readme.md"}},
		{"**/schema*.json", []string{"schema.json", "docs/contracts/schema-v2.json"}, []string{"docs/schemas/a.json"}},
		{"**/openapi*.{yml,yaml,json}", []string{"openapi.yaml", "api/openapi-v1.yml", "api/openapi.json"}, []string{"api/openapi.toml", "openapi/x.yaml"}},
		{"Dockerfile", []string{"Dockerfile", "apps/vloer/Dockerfile"}, []string{"Dockerfile.dev", "docs/Dockerfile.md"}},
		{"**/helm/**", []string{"ops/helm/ploeg/values.yaml"}, []string{"helmfile.yaml"}},
		{"**/.forgejo/workflows/**", []string{".forgejo/workflows/on_push.yml"}, []string{".forgejo/actions/x.yml"}},
		{"*.lock", []string{"yarn.lock", "deep/Cargo.lock"}, []string{"lock.go"}},
		{"ops/", []string{"ops/a", "ops/b/c"}, []string{"apps/ops/a"}},
		{"/pkg/rarity/*.go", []string{"pkg/rarity/paths.go"}, []string{"pkg/rarity/sub/x.go", "apps/pkg/rarity/x.go"}},
		{"pkg/store/card_?arity.go", []string{"pkg/store/card_rarity.go"}, []string{"pkg/store/card_grade.go"}},
	} {
		p, err := Compile(tc.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		for _, f := range tc.match {
			if !p.Match(f) {
				t.Errorf("%q does not match %q", tc.pattern, f)
			}
		}
		for _, f := range tc.miss {
			if p.Match(f) {
				t.Errorf("%q matches %q", tc.pattern, f)
			}
		}
	}
}

func TestPatternCompileRefusesBadPatterns(t *testing.T) {
	for _, bad := range []string{"", " a", "a//b", "a**/b", "{a,b", "a}", "{a,{b}}", "[", strings.Repeat("a", MaxPatternLength+1), "{a,b}{c,d}{e,f}{g,h}{i,j}{k,l}{m,n}"} {
		if _, err := Compile(bad); err == nil {
			t.Errorf("Compile(%q) accepted a bad pattern", bad)
		}
	}
}

func TestDefaultRules(t *testing.T) {
	m, err := Rules{}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"apps/ploeg/pkg/store/migrations/0031_card_rarity.sql", "db/schema.json", "api/v1/service.proto",
		"openapi.yaml", "Dockerfile", "apps/ploeg/ops/helm/ploeg/values.yaml", ".github/workflows/ci.yml", ".forgejo/workflows/on_push.yml"} {
		if !m.Sensitive(f) {
			t.Errorf("%s is not sensitive by default", f)
		}
	}
	for _, f := range []string{"pkg/store/card.go", "README.md", "docs/helmet.md"} {
		if m.Sensitive(f) {
			t.Errorf("%s is sensitive by default", f)
		}
	}
	for _, f := range []string{"package-lock.json", "apps/vloer/package-lock.json", "go.sum", "apps/ploeg/go.sum", "pnpm-lock.yaml",
		"api/v1/service.pb.go", "vendor/github.com/x/y.go", "web/node_modules/a/index.js", "public/app.min.js", "test/__snapshots__/a.snap", "mise.lock"} {
		if !m.Excluded(f) {
			t.Errorf("%s is counted in size by default", f)
		}
	}
	for _, f := range []string{"pkg/store/card.go", "go.mod", "package.json", "docs/vendor-notes.md"} {
		if m.Excluded(f) {
			t.Errorf("%s is left out of size by default", f)
		}
	}
}

func TestRulesReplaceDefaultsAndAddAttentionPaths(t *testing.T) {
	m, err := Rules{SensitivePaths: []string{}, AttentionPaths: []string{"pkg/store/**", "**/budget*.go"}, SizeExclude: []string{"docs/**"}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if m.Sensitive("migrations/0001.sql") || !m.Sensitive("pkg/store/card.go") || !m.Sensitive("pkg/worker/budget_test.go") {
		t.Error("an empty sensitivePaths keeps no default; attentionPaths are sensitive")
	}
	if m.Excluded("go.sum") || !m.Excluded("docs/index.md") {
		t.Error("sizeExclude replaces the default list")
	}
	m, err = Rules{AttentionPaths: []string{"pkg/httpapi/**"}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Sensitive("migrations/0001.sql") || !m.Sensitive("pkg/httpapi/server.go") || !m.Excluded("go.sum") {
		t.Error("attentionPaths add to the defaults")
	}
	for _, bad := range []Rules{
		{AttentionPaths: []string{"a", "a"}},
		{SizeExclude: []string{"["}},
		{SensitivePaths: make([]string, MaxPatterns+1)},
	} {
		if _, err := bad.Compile(); err == nil {
			t.Errorf("rules %+v compiled", bad)
		}
	}
}

func TestModule(t *testing.T) {
	for file, want := range map[string]string{
		"README.md":                      ".",
		"pkg/store/card.go":              "pkg",
		"docs/index.md":                  "docs",
		"apps/ploeg/pkg/store/card.go":   "apps/ploeg",
		"apps/README.md":                 "apps",
		"packages/ui/src/button.tsx":     "packages/ui",
		"services/api/main.go":           "services/api",
		"/cmd/ploegd/main.go":            "cmd",
		"crates/core/src/lib.rs":         "crates/core",
		"internal/ledger/adr_test.go":    "internal",
		".forgejo/workflows/on_push.yml": ".forgejo",
	} {
		if got := Module(file); got != want {
			t.Errorf("Module(%q) = %q; want %q", file, got, want)
		}
	}
}

func TestCountedLines(t *testing.T) {
	m, err := Rules{}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	n := func(v int) *int { return &v }
	total := func(v int64) *int64 { return &v }
	lock := FileLines{Path: "go.sum", Additions: n(40), Deletions: n(10)}
	code := FileLines{Path: "a.go", Additions: n(12), Deletions: n(3)}
	uncounted := FileLines{Path: "b.go"}
	for _, tc := range []struct {
		name      string
		files     []FileLines
		total     *int64
		truncated bool
		want      *int64
	}{
		{"excluded files count nothing", []FileLines{lock, code}, total(65), false, total(15)},
		{"a truncated list takes the excluded lines off the total", []FileLines{lock}, total(100), true, total(50)},
		{"an uncounted file falls back to the total without exclusions", []FileLines{code, uncounted}, total(30), false, total(30)},
		{"an uncounted file next to an excluded one is unknown", []FileLines{lock, uncounted}, total(30), false, nil},
		{"a truncated list without a total is unknown", []FileLines{code}, nil, true, nil},
	} {
		got := m.CountedLines(tc.files, tc.total, tc.truncated)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

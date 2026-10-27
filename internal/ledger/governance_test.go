package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sandboxMaintainerColumns = []string{"Name", "GitHub ID", "Company/Organization"}

var governanceFiles = []string{
	"GOVERNANCE.md",
	"MAINTAINERS.md",
	"CONTRIBUTOR_LADDER.md",
	"CONTRIBUTING.md",
	"CODE_OF_CONDUCT.md",
	"SECURITY.md",
}

var independentConductRoute = []string{
	"https://github.com/cncf/foundation/blob/main/code-of-conduct.md",
	"conduct@cncf.io",
}

func repositoryFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func firstTable(markdown string) []string {
	var rows []string
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			rows = append(rows, trimmed)
			continue
		}
		if len(rows) > 0 {
			break
		}
	}
	return rows
}

func tableCells(row string) []string {
	inner := strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	cells := strings.Split(inner, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func maintainerTableProblems(maintainers string) []string {
	rows := firstTable(maintainers)
	if len(rows) < 3 {
		return []string{"the first table has no maintainer rows"}
	}
	var problems []string
	header := tableCells(rows[0])
	for i, column := range sandboxMaintainerColumns {
		if i >= len(header) || header[i] != column {
			problems = append(problems, "column "+column+" is missing or out of place")
		}
	}
	for _, row := range rows[2:] {
		cells := tableCells(row)
		switch {
		case len(cells) < len(sandboxMaintainerColumns):
			problems = append(problems, "row has too few cells: "+row)
		case cells[0] == "":
			problems = append(problems, "row has no name: "+row)
		case !strings.HasPrefix(cells[1], "[@"):
			problems = append(problems, "row has no linked GitHub handle: "+row)
		case cells[2] == "":
			problems = append(problems, "row has no Company/Organization: "+row)
		}
	}
	return problems
}

func missingReadmeLinks(readme string) []string {
	var missing []string
	for _, file := range governanceFiles {
		if !strings.Contains(readme, "]("+file+")") {
			missing = append(missing, file)
		}
	}
	return missing
}

func missingConductRoutes(codeOfConduct string) []string {
	var missing []string
	for _, route := range independentConductRoute {
		if !strings.Contains(codeOfConduct, route) {
			missing = append(missing, route)
		}
	}
	return missing
}

func TestMaintainerTableProblems(t *testing.T) {
	cases := []struct {
		name       string
		markdown   string
		wantFailed bool
	}{
		{"sandbox columns with an independent maintainer pass", "# Maintainers\n\n| Name | GitHub ID | Company/Organization | Role |\n| --- | --- | --- | --- |\n| A Person | [@a](https://github.com/a) | Independent | Maintainer |\n", false},
		{"a missing Company/Organization column fails", "| Name | GitHub ID | Role |\n| --- | --- | --- |\n| A Person | [@a](https://github.com/a) | Maintainer |\n", true},
		{"a renamed column fails", "| Maintainer | GitHub ID | Company/Organization |\n| --- | --- | --- |\n| A Person | [@a](https://github.com/a) | Independent |\n", true},
		{"an empty affiliation fails", "| Name | GitHub ID | Company/Organization |\n| --- | --- | --- |\n| A Person | [@a](https://github.com/a) |  |\n", true},
		{"an unlinked handle fails", "| Name | GitHub ID | Company/Organization |\n| --- | --- | --- |\n| A Person | a | Independent |\n", true},
		{"a table without maintainers fails", "| Name | GitHub ID | Company/Organization |\n| --- | --- | --- |\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := maintainerTableProblems(c.markdown)
			if failed := len(problems) > 0; failed != c.wantFailed {
				t.Errorf("failed = %v, want %v: %v", failed, c.wantFailed, problems)
			}
		})
	}
}

func TestMissingReadmeLinks(t *testing.T) {
	var all []string
	for _, file := range governanceFiles {
		all = append(all, "["+file+"]("+file+")")
	}
	if missing := missingReadmeLinks(strings.Join(all, " · ")); len(missing) != 0 {
		t.Errorf("a README linking every file reports %v missing", missing)
	}
	withoutLadder := strings.ReplaceAll(strings.Join(all, " · "), "(CONTRIBUTOR_LADDER.md)", "")
	if missing := missingReadmeLinks(withoutLadder); len(missing) != 1 || missing[0] != "CONTRIBUTOR_LADDER.md" {
		t.Errorf("a README without the ladder link reports %v missing", missing)
	}
}

func TestMissingConductRoutes(t *testing.T) {
	full := "Ploeg follows the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/main/code-of-conduct.md). Write to conduct@cncf.io."
	if missing := missingConductRoutes(full); len(missing) != 0 {
		t.Errorf("a code of conduct with both routes reports %v missing", missing)
	}
	if missing := missingConductRoutes("Write to the maintainer."); len(missing) != len(independentConductRoute) {
		t.Errorf("a code of conduct with neither route reports only %v missing", missing)
	}
}

func TestMaintainersFileCarriesTheSandboxColumns(t *testing.T) {
	for _, problem := range maintainerTableProblems(repositoryFile(t, "MAINTAINERS.md")) {
		t.Errorf("MAINTAINERS.md: %s (the CNCF Sandbox application closes without them; ADR 0159)", problem)
	}
}

func TestReadmeLinksEveryGovernanceFile(t *testing.T) {
	for _, file := range missingReadmeLinks(repositoryFile(t, "README.md")) {
		t.Errorf("README.md does not link %s (ADR 0159)", file)
	}
	for _, file := range governanceFiles {
		repositoryFile(t, file)
	}
}

func TestCodeOfConductNamesTheIndependentRoute(t *testing.T) {
	for _, route := range missingConductRoutes(repositoryFile(t, "CODE_OF_CONDUCT.md")) {
		t.Errorf("CODE_OF_CONDUCT.md does not name %s, the route around a conflicted maintainer (ADR 0159)", route)
	}
}

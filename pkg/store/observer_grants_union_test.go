package store

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	observerFunction = regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION ploeg_grant_observer\(\).*?\$\$;`)
	observerGrant    = regexp.MustCompile(`(?s)GRANT SELECT\s*(?:\(([^)]*)\))?\s*ON\s+(.+?)\s+TO ploeg_observer`)
)

func observerGrants(sql string) map[string]map[string]bool {
	grants := map[string]map[string]bool{}
	for _, m := range observerGrant.FindAllStringSubmatch(sql, -1) {
		columns := []string{"*"}
		if strings.TrimSpace(m[1]) != "" {
			columns = strings.Split(m[1], ",")
		}
		for _, table := range strings.Split(m[2], ",") {
			table = strings.TrimSpace(table)
			if grants[table] == nil {
				grants[table] = map[string]bool{}
			}
			for _, c := range columns {
				grants[table][strings.TrimSpace(c)] = true
			}
		}
	}
	return grants
}

// Every migration that redefines ploeg_grant_observer() replaces the whole
// function, so the last definition must carry every grant an earlier one
// made. Two branches that each redefine it must, whichever merges last,
// carry the union of both.
func TestTheLastObserverGrantFunctionCarriesEveryEarlierGrant(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	definitions := map[string]map[string]map[string]bool{}
	for _, e := range entries {
		raw, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		body := observerFunction.FindString(string(raw))
		if body == "" {
			continue
		}
		names = append(names, e.Name())
		definitions[e.Name()] = observerGrants(body)
	}
	slices.Sort(names)
	if len(names) == 0 {
		t.Fatal("no migration defines ploeg_grant_observer()")
	}
	last := definitions[names[len(names)-1]]
	for _, name := range names[:len(names)-1] {
		for table, columns := range definitions[name] {
			for column := range columns {
				if !last[table][column] && !last[table]["*"] {
					t.Errorf("%s grants %s on %s, which the last definition in %s drops", name, column, table, names[len(names)-1])
				}
			}
		}
	}
}

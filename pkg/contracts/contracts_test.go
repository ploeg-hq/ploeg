package contracts

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

var (
	versionedFile   = regexp.MustCompile(`^(.+)\.v([1-9][0-9]*)\.schema\.json$`)
	unversionedFile = regexp.MustCompile(`^(.+)\.schema\.json$`)
	titleVersion    = regexp.MustCompile(` ([1-9][0-9]*)\.[0-9]+$`)
)

func publishedMajors(t *testing.T, dir string) map[string][]int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	majors := map[string][]int{}
	for _, file := range files {
		base := filepath.Base(file)
		if m := versionedFile.FindStringSubmatch(base); m != nil {
			major, _ := strconv.Atoi(m[2])
			majors[m[1]] = append(majors[m[1]], major)
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		m := titleVersion.FindStringSubmatch(doc.Title)
		if m == nil {
			t.Fatalf("%s names no major version: name the file <contract>.v<N>.schema.json, or end its title with <N>.<M>", base)
		}
		major, _ := strconv.Atoi(m[1])
		name := unversionedFile.FindStringSubmatch(base)[1]
		majors[name] = append(majors[name], major)
	}
	for name := range majors {
		slices.Sort(majors[name])
	}
	return majors
}

func TestServedMatchesThePublishedSchemas(t *testing.T) {
	published := publishedMajors(t, "../../docs/contracts")
	listed := map[string][]int{}
	for _, c := range Served() {
		listed[c.Name] = c.Versions
	}
	for name, majors := range published {
		if !slices.Equal(listed[name], majors) {
			t.Errorf("docs/contracts publishes %s at majors %v, but contracts.Served lists %v", name, majors, listed[name])
		}
	}
	for name, majors := range listed {
		if _, ok := published[name]; !ok {
			t.Errorf("contracts.Served lists %s %v, which docs/contracts does not publish", name, majors)
		}
	}
}

func TestServedIsSortedAndCannotBeChangedByACaller(t *testing.T) {
	first := Served()
	if !slices.IsSortedFunc(first, func(a, b Contract) int { return cmp.Compare(a.Name, b.Name) }) {
		t.Fatalf("Served is not sorted by name: %v", first)
	}
	first[0].Versions[0] = 99
	if Served()[0].Versions[0] == 99 {
		t.Fatal("a caller changed the served versions through the returned slice")
	}
}

func TestLookupFindsAServedMajor(t *testing.T) {
	c, ok := Lookup("operator-api")
	if !ok || !c.Serves(1) || c.Serves(2) {
		t.Fatalf("operator-api: %v %v", c, ok)
	}
	if _, ok := Lookup("no-such-contract"); ok {
		t.Fatal("an unknown contract was found")
	}
}

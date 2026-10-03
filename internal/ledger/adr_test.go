package ledger

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The ADR corpus and its Records index (docs/adrs/README.md) are two views of
// the same facts. These tests are the gate that keeps them agreeing, and they
// enforce the two local extensions to MADR 4.0 that docs/adrs/README.md
// defines:
//
//   - supersession is append-only — an accepted record's own status is never
//     flipped; the superseding record carries `supersedes: NNNN` and the index
//     surfaces "superseded by NNNN";
//   - a decision that can change carries `review-by: YYYY-MM-DD` plus a
//     "Re-evaluation triggers" section.
//
// The adr-writer skill's bundled validate_adr_consistency.py must NOT be used
// on this corpus: it assumes vanilla status-flip supersession and would reject
// every superseded record.

var (
	fileRe    = regexp.MustCompile(`^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
	rowRe     = regexp.MustCompile(`^\|\s*\[(\d{4})\]\(([^)]+)\)\s*\|(.*)\|\s*$`)
	isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

	baseStatuses = map[string]bool{
		"proposed": true, "accepted": true, "rejected": true, "deprecated": true,
	}
	skip = map[string]bool{"README.md": true, "adr-template.md": true}
)

type record struct {
	num, name, status, date, supersedes, reviewBy string
	hasConfirmation, hasTriggers                  bool
}

type row struct{ name, status, date string }

// adrDir resolves docs/adrs/ from this package's location, so the test is
// independent of the working directory `go test` happens to use.
func adrDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", ADRDir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("ADR corpus not found at %s: %v", dir, err)
	}
	return dir
}

func frontField(body, field string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(field) + `:\s*(.*?)\s*(?:#.*)?$`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}

func loadRecords(t *testing.T) map[string]record {
	t.Helper()
	dir := adrDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]record{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || skip[name] {
			continue
		}
		if strings.HasPrefix(name, "adr-") {
			t.Errorf("%s: `adr-` prefix is forbidden — records are never renamed", name)
			continue
		}
		m := fileRe.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s: not a NNNN-kebab-title.md record", name)
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(b)
		num := m[1]
		if prev, dup := out[num]; dup {
			t.Errorf("%s: number %s reused (%s exists)", name, num, prev.name)
			continue
		}
		out[num] = record{
			num:             num,
			name:            name,
			status:          frontField(body, "status"),
			date:            frontField(body, "date"),
			supersedes:      frontField(body, "supersedes"),
			reviewBy:        frontField(body, "review-by"),
			hasConfirmation: strings.Contains(body, "### Confirmation"),
			hasTriggers:     strings.Contains(body, "## Re-evaluation triggers"),
		}
	}
	if len(out) == 0 {
		t.Fatal("no ADR records found — the corpus should never be empty")
	}
	return out
}

func loadIndex(t *testing.T) map[string]row {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(adrDir(t), "README.md"))
	if err != nil {
		t.Fatalf("read Records index: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "## Records" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("docs/adrs/README.md: no '## Records' section")
	}
	out := map[string]row{}
	for _, l := range lines[start:] {
		m := rowRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		cells := strings.Split(m[3], "|")
		if len(cells) != 3 { // Decision | Status | Last updated
			continue
		}
		out[m[1]] = row{
			name:   m[2],
			status: strings.TrimSpace(cells[1]),
			date:   strings.TrimSpace(cells[2]),
		}
	}
	return out
}

// supersessionGraph maps a superseded number to the number that replaced it,
// derived from the append-only `supersedes:` field rather than from any
// record's own status.
func supersessionGraph(t *testing.T, recs map[string]record) map[string]string {
	t.Helper()
	g := map[string]string{}
	for _, r := range recs {
		if r.supersedes == "" || r.supersedes == "none" {
			continue
		}
		if _, ok := recs[r.supersedes]; !ok {
			t.Errorf("%s: supersedes %q which does not exist", r.name, r.supersedes)
			continue
		}
		if prev, dup := g[r.supersedes]; dup {
			t.Errorf("%s and %s both supersede %s — a record is replaced once",
				recs[prev].name, r.name, r.supersedes)
			continue
		}
		g[r.supersedes] = r.num
	}
	return g
}

func TestADRFrontMatter(t *testing.T) {
	for _, r := range sorted(loadRecords(t)) {
		if !baseStatuses[r.status] {
			t.Errorf("%s: illegal status %q — supersession is index-only, never a "+
				"flipped file status (allowed: accepted, deprecated, proposed, rejected)",
				r.name, r.status)
		}
		if !isoDateRe.MatchString(r.date) {
			t.Errorf("%s: date %q is not YYYY-MM-DD", r.name, r.date)
		}
		if r.supersedes == "" {
			t.Errorf("%s: no `supersedes` field (use a number or 'none')", r.name)
		}
	}
}

// A decision nobody can check is a preference. Every record names its gate.
func TestADRHasConfirmation(t *testing.T) {
	for _, r := range sorted(loadRecords(t)) {
		if !r.hasConfirmation {
			t.Errorf("%s: no '### Confirmation' subsection — every record names how "+
				"compliance is checked (CI gate, review step, or script)", r.name)
		}
	}
}

// The thing design.md §8 had that vanilla MADR has no slot for: a dated review
// backed by observable triggers. A date with no triggers is a reminder, not a
// decision record.
func TestADRDatedReviewHasTriggers(t *testing.T) {
	for _, r := range sorted(loadRecords(t)) {
		switch {
		case r.reviewBy == "":
			t.Errorf("%s: no `review-by` field (use a date or 'none')", r.name)
		case r.reviewBy == "none":
			// A decision that only changes if the project changes shape.
		case !isoDateRe.MatchString(r.reviewBy):
			t.Errorf("%s: review-by %q is neither 'none' nor YYYY-MM-DD", r.name, r.reviewBy)
		case !r.hasTriggers:
			t.Errorf("%s: review-by %s but no '## Re-evaluation triggers' section — "+
				"a dated review with no named trigger is a reminder, not a decision record",
				r.name, r.reviewBy)
		}
	}
}

func TestADRIndexParity(t *testing.T) {
	recs, idx := loadRecords(t), loadIndex(t)
	graph := supersessionGraph(t, recs)

	for _, r := range sorted(recs) {
		got, ok := idx[r.num]
		if !ok {
			t.Errorf("%s: no Records row — add it in the same commit", r.name)
			continue
		}
		if got.name != r.name {
			t.Errorf("%s: Records link %q != file %q", r.num, got.name, r.name)
		}
		want := r.status
		if by, superseded := graph[r.num]; superseded {
			want = "superseded by " + by
		}
		if got.status != want {
			t.Errorf("%s: Records status %q != expected %q", r.num, got.status, want)
		}
		if got.date != r.date {
			t.Errorf("%s: Records 'Last updated' %q != file date %q", r.num, got.date, r.date)
		}
	}
	for _, num := range sortedKeys(idx) {
		if _, ok := recs[num]; !ok {
			t.Errorf("Records row %s: no matching %s/%s-*.md", num, ADRDir, num)
		}
	}

	t.Logf("%d record(s), %d Records row(s), %d supersession(s)", len(recs), len(idx), len(graph))
}

// --- helpers ---

func sorted(m map[string]record) []record {
	out := make([]record, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

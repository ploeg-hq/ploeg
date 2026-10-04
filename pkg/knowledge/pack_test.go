package knowledge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/okf"
)

func concept(p, typ, title, desc string, tags []string, body string) okf.Concept {
	c := okf.Concept{Path: p, Type: typ, Title: title, Description: desc, Tags: tags, Body: body}
	c.Links = okf.Links(p, body)
	return c
}

func testBundle() okf.Bundle {
	return okf.Bundle{Name: "repo", Concepts: []okf.Concept{
		concept("conventions/migrations.md", "Convention", "Store migrations are append-only", "Add the next migration; never edit one.",
			[]string{"store", "postgres"}, "See [blackboard](/decisions/blackboard.md)."),
		concept("decisions/blackboard.md", "Decision", "The pull request is the blackboard", "Findings travel on the PR.",
			[]string{"adr"}, "Durable state is Postgres and git."),
		concept("pitfalls/registry.md", "Pitfall", "Registry pulls time out", "The sandbox reaches only its gateway.",
			[]string{"sandbox", "helm"}, "Skip the gate."),
		concept("facts/helm-chart.md", "Fact", "Chart location", "The sandbox chart is in ops.",
			[]string{"sandbox", "helm"}, "ops/ploeg."),
		concept("facts/unrelated.md", "Fact", "Release badges", "README badges.", nil, "Shields."),
	}}
}

func paths(p Pack) []string { return p.Paths() }

func TestSelect_RanksMatchesAndFollowsLinks(t *testing.T) {
	p := Select([]Source{{Name: "repo", Bundle: testBundle()}}, Query{Title: "Add a migration to the store", Description: "a postgres column"}, 0)
	if want := []string{"repo/conventions/migrations.md", "repo/decisions/blackboard.md"}; !reflect.DeepEqual(paths(p), want) {
		t.Fatalf("selected %v, want %v", paths(p), want)
	}
	if !strings.HasPrefix(p.Selected[0].Reason, "matches ") || !strings.Contains(p.Selected[0].Reason, "migration") {
		t.Errorf("direct match reason = %q", p.Selected[0].Reason)
	}
	if p.Selected[1].Reason != "matches postgre" && !strings.HasPrefix(p.Selected[1].Reason, "linked from ") {
		t.Errorf("linked concept reason = %q", p.Selected[1].Reason)
	}
	if p.Considered != 5 || p.Omitted != 0 {
		t.Errorf("considered %d omitted %d", p.Considered, p.Omitted)
	}
}

func TestSelect_IncludesNothingForUnrelatedWork(t *testing.T) {
	p := Select([]Source{{Name: "repo", Bundle: testBundle()}}, Query{Title: "Translate the login page"}, 0)
	if len(p.Selected) != 0 {
		t.Errorf("selected %v for unrelated work", paths(p))
	}
}

func TestSelect_PrefersAPitfallOnATie(t *testing.T) {
	p := Select([]Source{{Name: "repo", Bundle: testBundle()}}, Query{Title: "sandbox helm"}, 0)
	if len(p.Selected) < 2 || p.Selected[0].Concept.Type != PitfallType {
		t.Errorf("selected %v, want the pitfall first", paths(p))
	}
}

func TestSelect_MatchesLabelsToTags(t *testing.T) {
	p := Select([]Source{{Name: "repo", Bundle: testBundle()}}, Query{Title: "Something vague", Labels: []string{"Postgres"}}, 0)
	if len(p.Selected) == 0 || p.Selected[0].Concept.Path != "conventions/migrations.md" {
		t.Errorf("selected %v", paths(p))
	}
}

func TestSelect_KeepsTheBestWithinTheBudget(t *testing.T) {
	src := []Source{{Name: "repo", Bundle: testBundle()}}
	q := Query{Title: "Add a migration to the store", Description: "a postgres column"}
	first := len(okf.Format(testBundle().Concepts[0]))
	p := Select(src, q, first)
	if want := []string{"repo/conventions/migrations.md"}; !reflect.DeepEqual(paths(p), want) || p.Omitted != 1 || p.Bytes != first {
		t.Errorf("selected %v omitted %d bytes %d", paths(p), p.Omitted, p.Bytes)
	}
}

func TestDigest_ChangesWithContentNotOrder(t *testing.T) {
	b := testBundle()
	reversed := okf.Bundle{Name: b.Name}
	for i := len(b.Concepts) - 1; i >= 0; i-- {
		reversed.Concepts = append(reversed.Concepts, b.Concepts[i])
	}
	if Digest(b) != Digest(reversed) {
		t.Error("digest depends on order")
	}
	changed := testBundle()
	changed.Concepts[0].Body += " edited"
	if Digest(b) == Digest(changed) {
		t.Error("digest ignores an edit")
	}
}

func TestPackWrite_WritesAnIndexAndTheConceptsPerSource(t *testing.T) {
	dir := t.TempDir()
	other := okf.Bundle{Name: "team", Concepts: []okf.Concept{concept("store.md", "Fact", "Store notes", "postgres store hints", nil, "store migration hints")}}
	p := Select([]Source{{Name: "repo", Bundle: testBundle()}, {Name: "team", Bundle: other}}, Query{Title: "store migration postgres"}, 0)
	index, err := p.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"index.md", "repo/conventions/migrations.md", "repo/index.md", "team/store.md"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	for _, want := range []string{"(repo/conventions/migrations.md)", "(team/store.md)", "Sources: repo sha256:", ", team sha256:"} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q:\n%s", want, index)
		}
	}
}

func TestOmnigraph_RoundTripsABundle(t *testing.T) {
	b := testBundle()
	b.Concepts[0].Resource = "https://example.test/migrations"
	ndjson := ToOmnigraph(b, "test-agent")

	seenNode := map[string]bool{}
	var rows []map[string]any
	edges := 0
	sc := bufio.NewScanner(bytes.NewReader(ndjson))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var line struct {
			Type, Edge, From, To string
			Data                 map[string]any
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("not JSON: %s", sc.Bytes())
		}
		if line.Type != "" {
			if edges > 0 {
				t.Errorf("node %s after an edge", line.Type)
			}
			for _, k := range []string{"slug", "name", "url"} {
				if v, ok := line.Data[k].(string); ok {
					seenNode[v] = true
				}
			}
			if line.Type == "Note" {
				rows = append(rows, map[string]any{"slug": line.Data["slug"], "body": line.Data["body"]})
			}
			continue
		}
		edges++
		if !seenNode[line.From] || !seenNode[line.To] {
			t.Errorf("edge %s %s -> %s names a node the load does not hold", line.Edge, line.From, line.To)
		}
	}
	if !bytes.Contains(ndjson, []byte(`"edge":"Relates","from":"okf/repo/conventions/migrations.md","to":"okf/repo/decisions/blackboard.md"`)) {
		t.Error("the link is not a Relates edge")
	}
	if !bytes.Contains(ndjson, []byte(`"edge":"Cites"`)) {
		t.Error("the resource is not a Cites edge")
	}

	result, _ := json.Marshal(map[string]any{"rows": rows})
	back, problems, err := FromOmnigraph("repo", result)
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems %v err %v", problems, err)
	}
	if Digest(back) != Digest(b) {
		t.Errorf("round trip changed the bundle: %s != %s", Digest(back), Digest(b))
	}
}

func TestFromOmnigraph_RefusesForeignAndEscapingNotes(t *testing.T) {
	rows := `[{"$n.slug":"okf/repo/../../etc.md","$n.body":"---\ntype: x\n---\n"},
	          {"slug":"okf/other/a.md","body":"---\ntype: x\n---\n"},
	          {"slug":"okf/repo/ok.md","body":"---\ntype: x\n---\nfine\n"}]`
	b, problems, err := FromOmnigraph("repo", []byte(rows))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Concepts) != 1 || b.Concepts[0].Path != "ok.md" || len(problems) != 2 {
		t.Errorf("concepts %v problems %v", b.Concepts, problems)
	}
}

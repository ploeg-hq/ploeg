package cardimage

import (
	"bytes"
	"encoding/xml"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func mergedCard() store.OperatorCard {
	merged := now.Add(-9 * 24 * time.Hour)
	return store.OperatorCard{
		WorkItemID: "42", Title: "Add a retry budget to the forge client", ExternalRef: "VIK-1701", Team: "silver",
		Target: &store.OperatorCardTarget{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg"},
		Style:  store.CardStyle{Skin: "default"},
		State:  "merged", Finish: "matte",
		Steward: &store.CardSteward{Name: "anna", Source: "merged_by"},
		Roster:  []store.CardPerson{{Name: "anna", Roles: []string{"merger"}}, {Name: "bram", Roles: []string{"reviewer", "qa"}}},
		Crew: []store.CardCrew{
			{Role: "builder", Writes: true, Runs: 2, CostUSD: ptr(1.1)},
			{Role: "reviewer", Runs: 1, CostUSD: ptr(0.14)},
		},
		Plays: []store.CardPlay{{
			Number: 57, State: "merged", MergedAt: &merged, MergedBy: "anna",
			Additions: ptr(1240), Deletions: ptr(31), ChangedFiles: ptr(7),
			CI: &store.CardCI{State: "success"},
		}},
		Totals:  store.CardTotals{CostUSD: ptr(1.24), AuthorizedUSD: 5, CostStatus: "observed", Runs: 3},
		Release: &store.CardRelease{At: merged.Add(2 * time.Hour), Source: "deploy", Environment: "production"},
	}
}

func graded(c store.OperatorCard) store.OperatorCard {
	c.Grade = &store.CardGrade{Formula: "2026.2", Overall: 8.5, Provisional: true, Qualifiers: []string{"RT"}}
	return c
}

func cracked(c store.OperatorCard, mended bool) store.OperatorCard {
	c = graded(c)
	crack := store.CardCrack{ID: "1", Bug: store.CardCrackBug{WorkItemID: "77", Title: "Retry loops forever"}, Severity: "S2",
		ConfirmedBy: []string{"carla"}, Weight: 2}
	state := "cracked"
	if mended {
		state = "mended"
		at := now.Add(-time.Hour)
		crack.Mended = &store.CardCrackMend{At: at, By: "dirk", PR: 61, ConfirmedAt: &at}
	}
	c.Condition = &store.CardCondition{State: state, Cracks: []store.CardCrack{crack}}
	return c
}

func goldenCases() map[string]store.OperatorCard {
	escaping := mergedCard()
	escaping.Title = `<script>alert("x")</script> & 'quotes' @carla #12 ` + "\u202e" + `evil` + "\x00"
	escaping.Steward = &store.CardSteward{Name: `<b>ann&a</b>`, Source: "approver"}
	escaping.ExternalRef = `VIK-1"/><x`
	escaping.Crew = []store.CardCrew{{Role: `<img src=x>`, Runs: 1}}

	demo := mergedCard()
	demo.Demo = true
	demo.Totals = store.CardTotals{AuthorizedUSD: 5, CostStatus: "not_reported"}
	demo.Release = nil

	drafting := store.OperatorCard{WorkItemID: "9", Title: "A very long title that keeps going well past what three lines of the card can hold without help", State: "drafting",
		Totals: store.CardTotals{CostStatus: "not_reported"}}

	over := graded(mergedCard())
	over.Totals.CostUSD = ptr(6.5)
	over.Style.Skin = "forge"
	over.Release = &store.CardRelease{At: now.Add(-400 * 24 * time.Hour), Source: "merge", Environment: "production"}
	gold := "gold"
	over.Grade = &store.CardGrade{Formula: "2026.2", Overall: 10, Label: &gold, Qualifiers: []string{"OB"}}

	return map[string]store.OperatorCard{
		"merged":   mergedCard(),
		"escaping": escaping,
		"demo":     demo,
		"drafting": drafting,
		"graded":   graded(mergedCard()),
		"cracked":  cracked(mergedCard(), false),
		"mended":   cracked(mergedCard(), true),
		"forge":    over,
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./pkg/cardimage -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from its golden file; run go test ./pkg/cardimage -update and review the diff", name)
	}
}

func TestRenderMatchesTheGoldenImages(t *testing.T) {
	for name, card := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			svg := Render(card, Options{Now: now})
			assertWellFormed(t, svg)
			golden(t, name+".svg", svg)
			golden(t, name+".md", []byte(Summary(card, now)))
		})
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	card := cracked(mergedCard(), true)
	if a, b := Render(card, Options{Now: now}), Render(card, Options{Now: now}); !bytes.Equal(a, b) {
		t.Fatal("two renders of the same card differ")
	}
}

func assertWellFormed(t *testing.T, svg []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(svg))
	d.Strict = true
	elements := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("image is not well-formed XML: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			elements++
			for _, banned := range []string{"script", "foreignObject", "image", "a", "style"} {
				if se.Name.Local == banned {
					t.Errorf("image contains a <%s> element", banned)
				}
			}
			for _, attr := range se.Attr {
				if strings.HasPrefix(strings.ToLower(attr.Name.Local), "on") || strings.Contains(attr.Value, "javascript:") {
					t.Errorf("image carries attribute %s=%q", attr.Name.Local, attr.Value)
				}
			}
		}
	}
	if elements < 10 {
		t.Fatalf("image has %d elements", elements)
	}
}

func TestRenderEscapesEveryCardText(t *testing.T) {
	card := goldenCases()["escaping"]
	svg := string(Render(card, Options{Now: now}))
	for _, raw := range []string{"<script", "<b>", "<img", `"/><x`, "\u202e", "\x00"} {
		if strings.Contains(svg, raw) {
			t.Errorf("image contains unescaped %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;b&gt;ann&amp;a&lt;/b&gt;", "&lt;img src=x&gt;"} {
		if !strings.Contains(svg, escaped) {
			t.Errorf("image lacks escaped %q", escaped)
		}
	}
	summary := Summary(card, now)
	for _, raw := range []string{"<script", "<b>", "<img", "@carla", "#12", "\u202e"} {
		if strings.Contains(summary, raw) {
			t.Errorf("summary contains %q, which a forge would render, mention or link", raw)
		}
	}
}

func TestDemoCardShowsNoCost(t *testing.T) {
	card := goldenCases()["demo"]
	svg := string(Render(card, Options{Now: now}))
	summary := Summary(card, now)
	for _, out := range []string{svg, summary} {
		if strings.Contains(out, "US$") {
			t.Errorf("demo card shows an amount:\n%s", out)
		}
		if !strings.Contains(out, "Demo") || !strings.Contains(out, "no model calls") {
			t.Errorf("demo card does not say it is a demo:\n%s", out)
		}
	}
}

func TestGradedCrackedAndMendedCardsCarryTheirMarks(t *testing.T) {
	cases := goldenCases()
	if svg := string(Render(cases["merged"], Options{Now: now})); strings.Contains(svg, `data-slot="grade"`) || strings.Contains(svg, "data-crack") {
		t.Error("an ungraded, uncracked card draws a slab or a crack")
	}
	if svg := string(Render(cases["graded"], Options{Now: now})); !strings.Contains(svg, `data-slot="grade"`) || !strings.Contains(svg, ">8.5<") {
		t.Error("graded card lacks its slab")
	}
	crackedSVG := string(Render(cases["cracked"], Options{Now: now}))
	if !strings.Contains(crackedSVG, `data-crack="1"`) || !strings.Contains(crackedSVG, "Cracked · 1 crack") {
		t.Error("cracked card lacks its crack")
	}
	mendedSVG := string(Render(cases["mended"], Options{Now: now}))
	gold := cardPalette.gold
	if !strings.Contains(mendedSVG, `stroke="`+gold+`" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" data-crack="1"`) || !strings.Contains(mendedSVG, "Mended · 1 crack sealed in gold") {
		t.Error("mended card lacks its gold seam")
	}
}

func TestSummaryNamesNoPersonButTheSteward(t *testing.T) {
	summary := Summary(cracked(mergedCard(), true), now)
	if !strings.Contains(summary, "anna") {
		t.Error("summary lacks the steward")
	}
	for _, person := range []string{"bram", "carla", "dirk"} {
		if strings.Contains(summary, person) {
			t.Errorf("summary names %s, who is not the steward", person)
		}
	}
	svg := string(Render(cracked(mergedCard(), true), Options{Now: now}))
	for _, person := range []string{"bram", "carla", "dirk", "Retry loops forever"} {
		if strings.Contains(svg, person) {
			t.Errorf("image names %q", person)
		}
	}
}

func TestTheConsumersSkinDoesNotChangePloegsImage(t *testing.T) {
	plain := graded(mergedCard())
	want := Render(plain, Options{Now: now})
	for _, skin := range []string{"default", "forge", "no-such-skin"} {
		styled := plain
		styled.Style = store.CardStyle{Skin: skin}
		if got := Render(styled, Options{Now: now}); !bytes.Equal(got, want) {
			t.Errorf("skin %q changed the image Ploeg posts", skin)
		}
	}
}

func TestMomentKeysChangeOnlyAtMoments(t *testing.T) {
	open := mergedCard()
	open.Plays[0].State = "open"
	open.State = "in_review"
	if m := MomentOf(open, now); m.Key != "" || m.Play != 0 {
		t.Errorf("unmerged card has moment %+v", m)
	}

	merged := mergedCard()
	merged.Release = nil
	m := MomentOf(merged, now)
	if m.Key != "merged:57" || m.Play != 57 || m.Headline("") != "Merged" {
		t.Errorf("merged moment = %+v %q", m, m.Headline(""))
	}

	released := mergedCard()
	released.Release.At = now.Add(-time.Hour)
	r := MomentOf(released, now)
	if r.Key != "merged:57;released:production;finish:matte" || r.Headline(m.Key) != "Released to production" {
		t.Errorf("released moment = %q %q", r.Key, r.Headline(m.Key))
	}
	if again := MomentOf(released, now.Add(5*24*time.Hour)); again.Key != r.Key {
		t.Errorf("days passing below the next finish moved the moment to %q", again.Key)
	}
	foil := MomentOf(released, now.Add(7*24*time.Hour))
	if foil.Key != "merged:57;released:production;finish:foil" || foil.Headline(r.Key) != "Foil finish" {
		t.Errorf("foil moment = %q %q", foil.Key, foil.Headline(r.Key))
	}

	fromMerge := mergedCard()
	fromMerge.Release.Source = "merge"
	if k := MomentOf(fromMerge, now).Key; k != "merged:57;finish:foil" {
		t.Errorf("a release counted from the merge is not a release moment: %q", k)
	}

	crackedKey := MomentOf(cracked(mergedCard(), false), now).Key
	if crackedKey != MomentOf(mergedCard(), now).Key {
		t.Errorf("a crack alone moved the moment to %q", crackedKey)
	}
	mended := MomentOf(cracked(mergedCard(), true), now)
	if mended.Key != crackedKey+";mended:1" || mended.Headline(crackedKey) != "Mended" {
		t.Errorf("mended moment = %q %q", mended.Key, mended.Headline(crackedKey))
	}

	withdrawn := mergedCard()
	withdrawn.State = "withdrawn"
	if k := MomentOf(withdrawn, now).Key; k != "" {
		t.Errorf("withdrawn card has moment %q", k)
	}
}

func TestFinishLadderAndMoney(t *testing.T) {
	for days, want := range map[int]string{-1: "matte", 0: "matte", 6: "matte", 7: "foil", 29: "foil", 30: "holo", 90: "prism", 180: "gilded", 364: "gilded", 365: "infinity", 9000: "infinity"} {
		if got := FinishFor(days).Key; got != want {
			t.Errorf("FinishFor(%d) = %s, want %s", days, got, want)
		}
	}
	for v, want := range map[float64]string{0: "US$ 0,00", 0.004: "< US$ 0,01", 1.235: "US$ 1,24", 1234.5: "US$ 1.234,50", -2: "-US$ 2,00"} {
		if got := Money(v); got != want {
			t.Errorf("Money(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestCommentBodyMatchesItsGoldenFile(t *testing.T) {
	card := cracked(mergedCard(), true)
	golden(t, "comment.md", []byte(CommentBody(card, now, "Mended", "https://forge.example/attachments/abc")))
	if !strings.HasPrefix(CommentBody(card, now, "Merged", ""), CommentMarker+"\n") {
		t.Error("comment body does not open with the marker")
	}
}

func TestCommentBodyEmbedsOnlySafeImageURLs(t *testing.T) {
	card := goldenCases()["escaping"]
	for url, embedded := range map[string]bool{
		"https://forge.example/attachments/abc": true,
		"/uploads/abc/run-card-42.svg":          true,
		"":                                      false,
		"javascript:alert(1)":                   false,
		"//evil.example/x.svg":                  false,
		"https://x.example/a) [b](https://y":    false,
		"https://x.example/a\n<script>":         false,
	} {
		body := CommentBody(card, now, "Merged", url)
		if got := strings.Contains(body, "!["); got != embedded {
			t.Errorf("url %q embedded = %v, want %v", url, got, embedded)
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "@carla") {
			t.Errorf("url %q: body carries unescaped card text:\n%s", url, body)
		}
	}
	if got := CommentBody(card, now, "Released to <b>prod</b>", ""); !strings.Contains(got, "### Run card · Released to &lt;b&gt;prod&lt;/b&gt;") {
		t.Errorf("headline not escaped:\n%s", got)
	}
}

func TestFileNameKeepsOnlyTheWorkItemDigits(t *testing.T) {
	if got := FileName(store.OperatorCard{WorkItemID: "42/../x"}); got != "run-card-42.svg" {
		t.Errorf("FileName = %q", got)
	}
}

func TestCardCommentsAreFoundByTheirMarkerOfAnyRelease(t *testing.T) {
	for body, want := range map[string]bool{
		CommentBody(mergedCard(), now, "Merged", ""): true,
		"<!-- acme:run-card -->\n### Run card":       true,
		"  <!--ploeg:run-card-->":                    true,
		"<!-- ploeg:usage-report -->":                false,
		"A person quoting <!-- ploeg:run-card -->":   false,
	} {
		if got := IsCardComment(body); got != want {
			t.Errorf("IsCardComment(%q) = %v, want %v", body, got, want)
		}
	}
}

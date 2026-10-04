// Package cardimage draws a Run card as a static SVG image and as a compact
// Markdown summary, for the card comment Ploeg keeps on a pull request
// (ADR-0055). Rendering is pure: the same card, clock and skin give the same
// bytes on every host, and every text from a card is escaped.
package cardimage

import (
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Finish is one step of the finish ladder a released card climbs by staying
// live in production.
type Finish struct {
	Key   string
	Label string
	Days  int
	Level int
}

// FinishLadder holds every finish in climbing order.
var FinishLadder = []Finish{
	{Key: "matte", Label: "Matte", Days: 0, Level: 0},
	{Key: "foil", Label: "Foil", Days: 7, Level: 1},
	{Key: "holo", Label: "Holo", Days: 30, Level: 2},
	{Key: "prism", Label: "Prism", Days: 90, Level: 3},
	{Key: "gilded", Label: "Gilded", Days: 180, Level: 4},
	{Key: "infinity", Label: "Infinity", Days: 365, Level: 5},
}

// FinishFor returns the finish of a card that has been live for days whole
// days; a negative count is matte.
func FinishFor(days int) Finish {
	out := FinishLadder[0]
	for _, f := range FinishLadder {
		if days >= f.Days {
			out = f
		}
	}
	return out
}

// DaysLive counts the whole days since the card's release at now, or returns
// false when the card has no release. A release ahead of now counts 0.
func DaysLive(card store.OperatorCard, now time.Time) (int, bool) {
	if card.Release == nil {
		return 0, false
	}
	d := now.Sub(card.Release.At)
	if d < 0 {
		return 0, true
	}
	return int(d / (24 * time.Hour)), true
}

type tone string

const (
	toneNeutral   tone = "neutral"
	toneReview    tone = "review"
	toneSuccess   tone = "success"
	toneDanger    tone = "danger"
	toneAttention tone = "attention"
	toneLive      tone = "live"
)

type labelled struct {
	label string
	tone  tone
}

var cardStates = map[string]labelled{
	"drafting":  {"Drafting", toneNeutral},
	"in_review": {"In review", toneReview},
	"merged":    {"Merged", toneSuccess},
	"closed":    {"Closed unmerged", toneNeutral},
	"withdrawn": {"Withdrawn", toneNeutral},
}

var playStates = map[string]labelled{
	"open":   {"Open", toneReview},
	"merged": {"Merged", toneSuccess},
	"closed": {"Closed unmerged", toneNeutral},
}

var ciStates = map[string]labelled{
	"success": {"CI passed", toneSuccess},
	"failure": {"CI failed", toneDanger},
	"error":   {"CI errored", toneDanger},
	"pending": {"CI running", toneLive},
}

var stewardSources = map[string]string{
	"merged_by": "Merged the pull request",
	"approver":  "Approved the pull request",
}

type view struct {
	title      string
	demo       bool
	state      labelled
	cost       costView
	diff       string
	diffKnown  bool
	adds, dels string
	files      string
	pr         string
	prState    labelled
	ci         labelled
	ciKnown    bool
	released   bool
	days       int
	dayText    string
	finish     Finish
	source     string
	grade      *gradeView
	cracks     int
	mended     int
	condition  string
	steward    string
	stewardBy  string
	crew       string
	plays      string
	ids        []string
	repo       string
}

type costView struct {
	value   string
	caption string
	text    string
	share   float64
	arc     bool
	over    bool
}

type gradeView struct {
	overall     string
	label       string
	provisional bool
	formula     string
	qualifiers  string
}

func newView(card store.OperatorCard, now time.Time) view {
	v := view{title: strings.TrimSpace(card.Title), demo: card.Demo}
	if v.title == "" {
		v.title = "Work Item #" + card.WorkItemID
	}
	v.state = lookup(cardStates, card.State, "Drafting")
	v.cost = newCost(card)
	plays := card.Plays
	v.diffKnown, v.adds, v.dels, v.files = diffOf(plays)
	switch {
	case len(plays) == 0:
		v.diff = "No pull request yet"
	case !v.diffKnown:
		v.diff = "Not reported"
	default:
		v.diff = v.adds + " " + v.dels
		if v.files != "" {
			v.diff += " · " + v.files
		}
	}
	if len(plays) > 0 {
		latest := plays[len(plays)-1]
		v.pr = "#" + strconv.Itoa(latest.Number)
		v.prState = lookup(playStates, latest.State, "In review")
		if latest.State == "" {
			v.prState = labelled{"In review", toneReview}
		}
		if latest.CI != nil && latest.CI.State != "" && latest.CI.State != "unknown" {
			v.ci, v.ciKnown = lookup(ciStates, latest.CI.State, "CI not reported"), true
		}
	}
	if !v.ciKnown {
		v.ci = labelled{"CI not reported", toneNeutral}
	}
	if days, ok := DaysLive(card, now); ok {
		v.released, v.days = true, days
		v.dayText = "Day " + count(int64(days))
		v.source = card.Release.Source
	}
	v.finish = FinishFor(v.days)
	if !v.released {
		v.finish = FinishLadder[0]
	}
	if g := card.Grade; g != nil {
		gv := &gradeView{overall: strconv.FormatFloat(g.Overall, 'f', 1, 64), provisional: g.Provisional, formula: g.Formula,
			qualifiers: strings.Join(g.Qualifiers, " ")}
		if g.Label != nil {
			gv.label = strings.ToUpper((*g.Label)[:1]) + (*g.Label)[1:] + " Label"
		}
		v.grade = gv
	}
	if c := card.Condition; c != nil {
		v.condition = c.State
		v.cracks = len(c.Cracks)
		for _, k := range c.Cracks {
			if k.Mended != nil && k.Mended.ConfirmedAt != nil {
				v.mended++
			}
		}
	}
	if card.Steward != nil && strings.TrimSpace(card.Steward.Name) != "" {
		v.steward = strings.TrimSpace(card.Steward.Name)
		v.stewardBy = stewardSources[card.Steward.Source]
	}
	v.crew = crewLine(card.Crew)
	v.plays = "No plays yet"
	if len(plays) > 0 {
		v.plays = plural(int64(len(plays)), "play")
	}
	if card.Target != nil && card.Target.Owner != "" && card.Target.Repo != "" {
		v.repo = card.Target.Owner + "/" + card.Target.Repo
	}
	for _, id := range []string{prefixed("#", card.WorkItemID), strings.TrimSpace(card.ExternalRef), v.repo} {
		if id != "" {
			v.ids = append(v.ids, id)
		}
	}
	return v
}

func prefixed(prefix, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return prefix + strings.TrimSpace(s)
}

func lookup(table map[string]labelled, key, fallback string) labelled {
	if l, ok := table[key]; ok {
		return l
	}
	if key == "" {
		return labelled{fallback, toneNeutral}
	}
	words := strings.ReplaceAll(key, "_", " ")
	return labelled{strings.ToUpper(words[:1]) + words[1:], toneNeutral}
}

func newCost(card store.OperatorCard) costView {
	t := card.Totals
	of := ""
	if t.AuthorizedUSD > 0 {
		of = "of " + Money(t.AuthorizedUSD)
	}
	if card.Demo {
		return costView{value: "Demo", caption: "no model calls", text: "Demo · no model calls"}
	}
	if t.CostUSD == nil || t.CostStatus == "not_reported" {
		return costView{value: "Not reported", caption: of, text: "Not reported"}
	}
	cost := *t.CostUSD
	c := costView{value: Money(cost), text: Money(cost)}
	var caption []string
	if t.CostStatus == "reserved" {
		caption = append(caption, "reserved")
		c.text += " reserved"
	}
	if of != "" {
		caption = append(caption, of)
		c.text += " " + of
		c.share = min(1, cost/t.AuthorizedUSD)
		c.arc = c.share > 0
		c.over = cost > t.AuthorizedUSD
	}
	c.caption = strings.Join(caption, " · ")
	return c
}

func diffOf(plays []store.CardPlay) (bool, string, string, string) {
	var adds, dels, files int64
	measured, counted := 0, 0
	for _, p := range plays {
		if p.Additions == nil || p.Deletions == nil {
			continue
		}
		measured++
		adds += int64(*p.Additions)
		dels += int64(*p.Deletions)
		if p.ChangedFiles != nil {
			counted++
			files += int64(*p.ChangedFiles)
		}
	}
	if measured == 0 {
		return false, "", "", ""
	}
	f := ""
	if counted > 0 {
		f = plural(files, "file")
	}
	return true, "+" + count(adds), "−" + count(dels), f
}

func crewLine(crew []store.CardCrew) string {
	var parts []string
	for _, m := range crew {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			continue
		}
		if m.Runs > 0 {
			role += " ×" + strconv.Itoa(m.Runs)
		}
		parts = append(parts, role)
	}
	if len(parts) == 0 {
		return "No agent Runs yet"
	}
	return strings.Join(parts, " · ")
}

// Money formats US dollars as the card image shows them: nl-NL, two decimals,
// "US$ 1.234,50", and "< US$ 0,01" for a positive amount below a cent.
func Money(v float64) string {
	if v > 0 && v < 0.01 {
		return "< US$ 0,01"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	cents := int64(v*100 + 0.5)
	s := "US$ " + count(cents/100) + "," + pad2(cents%100)
	if neg {
		s = "-" + s
	}
	return s
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

func count(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int64, singular string) string {
	if n == 1 {
		return count(n) + " " + singular
	}
	return count(n) + " " + singular + "s"
}

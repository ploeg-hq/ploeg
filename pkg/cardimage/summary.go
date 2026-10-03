package cardimage

import (
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Summary is the compact Markdown table of the facts the card image shows,
// for readers whose forge does not show the image and for notification
// e-mails. It names no person except the steward, and every text from the
// card is escaped so it cannot add Markdown, HTML, a mention or a reference.
func Summary(card store.OperatorCard, now time.Time) string {
	v := newView(card, now)
	var b strings.Builder
	b.WriteString("| Card | " + md(v.title) + " |\n| --- | --- |\n")
	row := func(label, value string) { b.WriteString("| " + label + " | " + md(value) + " |\n") }
	row("State", v.state.label)
	row("Cost", v.cost.text)
	row("Diff", v.diff)
	if v.pr != "" {
		row("Pull request", v.pr+" · "+v.prState.label+" · "+v.ci.label)
	}
	if v.released {
		live := v.dayText + " · " + v.finish.Label + " finish"
		if v.source == "merge" {
			live += " · counted from merge, no deploy signal"
		}
		row("Days live", live)
	}
	if g := v.grade; g != nil {
		grade := g.overall
		switch {
		case g.label != "":
			grade += " · " + g.label
		case g.provisional:
			grade += " · provisional"
		}
		if g.qualifiers != "" {
			grade += " · " + g.qualifiers
		}
		row("Grade", grade+" · formula "+g.formula)
	}
	if v.cracks > 0 {
		condition := "Cracked · " + plural(int64(v.cracks), "crack") + " · " + count(int64(v.mended)) + " mended"
		if v.condition == "mended" {
			condition = "Mended · " + plural(int64(v.cracks), "crack") + " sealed"
		}
		row("Condition", condition)
	}
	steward := "Unsigned"
	if v.steward != "" {
		steward = v.steward
		if v.stewardBy != "" {
			steward += " · " + v.stewardBy
		}
	}
	row("Steward", steward)
	row("Crew", v.crew)
	row("Ids", strings.Join(v.ids, " · "))
	return b.String()
}

var mdReplacer = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;",
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]",
	"(", "\\(", ")", "\\)", "!", "\\!", "|", "\\|", "~", "\\~", "$", "\\$",
	"@", "@\u2060", "#", "#\u2060", ":", ":\u2060",
)

func md(s string) string {
	return strings.TrimSpace(mdReplacer.Replace(clean(s)))
}

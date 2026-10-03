package cardimage

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Width is the card image's width in CSS pixels. Its height grows with the
// slots the card fills.
const Width = 360

// ContentType is the media type of a rendered card.
const ContentType = "image/svg+xml"

// Options are what a card image takes besides the card.
type Options struct {
	// Now is the clock that counts days live; the image never reads the
	// system clock.
	Now time.Time
	// Skin is the Work Target's card skin. Only its colours are used; an
	// unknown skin draws with the default skin's colours.
	Skin string
}

const (
	pad      = 20
	font     = `Inter, -apple-system, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif`
	maxTitle = 3
)

type canvas struct {
	b bytes.Buffer
	p palette
}

func (c *canvas) f(format string, args ...any) { fmt.Fprintf(&c.b, format, args...) }

// Render draws card as a standalone SVG document in the layout of Vloer's
// Native card face: header, title, state and days live, cost ring, diff and
// pull request tiles, grade slab, crack and mend marks, crew line, steward and
// ids. Every text from the card is XML-escaped and truncated to its slot.
func Render(card store.OperatorCard, opts Options) []byte {
	v := newView(card, opts.Now)
	p := paletteFor(opts.Skin)
	body := &canvas{p: p}
	body.header(v)
	y := body.title(v)
	y = body.chips(v, y+14)
	y = body.main(v, y+18)
	if v.grade != nil {
		y = body.slab(v, y+12)
	}
	if v.cracks > 0 {
		y = body.conditionRow(v, y+12)
	}
	y = body.crewRow(v, y+14)
	y = body.stewardRow(v, y+12)
	height := y + 52

	c := &canvas{p: p}
	c.f(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="card-title card-desc">`,
		Width, height, Width, height)
	c.f(`<title id="card-title">Run card: %s</title>`, esc(v.title))
	c.f(`<desc id="card-desc">%s</desc>`, esc(describe(v)))
	c.f(`<defs><clipPath id="card-clip"><rect x="0" y="0" width="%d" height="%d" rx="14"/></clipPath>`, Width, height)
	c.f(`<linearGradient id="card-sheen" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#FFFFFF" stop-opacity="0"/><stop offset="0.5" stop-color="%s" stop-opacity="0.16"/><stop offset="1" stop-color="#FFFFFF" stop-opacity="0"/></linearGradient></defs>`,
		c.finishColour(v.finish))
	c.f(`<g font-family="%s">`, esc(font))
	c.frame(v, height)
	c.b.Write(body.b.Bytes())
	c.cracks(v)
	c.footer(v, height)
	c.f(`</g></svg>`)
	c.b.WriteByte('\n')
	return c.b.Bytes()
}

func (c *canvas) finishColour(f Finish) string {
	if col, ok := c.p.finish[f.Key]; ok {
		return col
	}
	return c.p.border
}

func (c *canvas) frame(v view, height int) {
	c.f(`<rect x="0.5" y="0.5" width="%d" height="%d" rx="14" fill="%s" stroke="%s"/>`, Width-1, height-1, c.p.surface, c.p.border)
	c.f(`<g clip-path="url(#card-clip)"><rect x="0" y="0" width="%d" height="4" fill="%s"/>`, Width, c.p.tones[v.state.tone])
	level := v.finish.Level
	if level >= 2 {
		c.f(`<rect x="0" y="0" width="%d" height="%d" fill="url(#card-sheen)"/>`, Width, height)
	}
	if level >= 3 {
		c.f(`<path d="M-40 %d L%d -40" stroke="%s" stroke-opacity="0.18" stroke-width="36"/>`, height-120, Width+40, c.finishColour(v.finish))
	}
	c.f(`</g>`)
	if level >= 1 {
		c.f(`<rect x="1.5" y="1.5" width="%d" height="%d" rx="13" fill="none" stroke="%s" stroke-width="3" data-finish="%s"/>`,
			Width-3, height-3, c.finishColour(v.finish), v.finish.Key)
	}
	if level >= 4 {
		c.f(`<rect x="7" y="7" width="%d" height="%d" rx="10" fill="none" stroke="%s" stroke-width="1"/>`, Width-14, height-14, c.p.gold)
	}
	if level >= 5 {
		c.f(`<rect x="4" y="4" width="%d" height="%d" rx="12" fill="none" stroke="%s" stroke-width="1" stroke-dasharray="2 4"/>`, Width-8, height-8, c.finishColour(v.finish))
	}
}

func (c *canvas) header(v view) {
	c.f(`<rect x="%d" y="20" width="32" height="32" rx="8" fill="%s"/>`, pad, c.p.selected)
	if v.pr != "" {
		c.f(`<g fill="none" stroke="%s" stroke-width="1.8" stroke-linecap="round"><circle cx="31" cy="29" r="2.5"/><circle cx="31" cy="43" r="2.5"/><circle cx="41" cy="43" r="2.5"/><path d="M31 31.5 V40.5 M41 40.5 V34 a4 4 0 0 0 -4 -4 H35"/></g>`, c.p.accentFg)
	} else {
		c.f(`<g fill="none" stroke="%s" stroke-width="1.8" stroke-linejoin="round"><rect x="29" y="29" width="14" height="14" rx="2"/><path d="M32 34 H40 M32 38 H37"/></g>`, c.p.accentFg)
	}
	c.f(`<text x="62" y="33" font-size="13" font-weight="700" fill="%s">Ticket card</text>`, c.p.text)
	sub := v.repo
	if sub == "" {
		sub = "No repository"
	}
	c.f(`<text x="62" y="49" font-size="12" fill="%s">%s</text>`, c.p.muted, esc(fit(sub, 32)))
	c.f(`<text x="%d" y="33" font-size="12" font-weight="800" letter-spacing="1.5" text-anchor="end" fill="%s">UNFOLD</text>`, Width-pad, c.p.accentFg)
	if v.demo {
		c.f(`<rect x="%d" y="39" width="44" height="16" rx="8" fill="%s" fill-opacity="0.16" stroke="%s"/>`, Width-pad-44, c.p.tones[toneAttention], c.p.tones[toneAttention])
		c.f(`<text x="%d" y="51" font-size="10" font-weight="700" text-anchor="middle" fill="%s">Demo</text>`, Width-pad-22, c.p.text)
	}
}

func (c *canvas) title(v view) int {
	lines := wrap(v.title, 30, maxTitle)
	y := 84
	for i, line := range lines {
		c.f(`<text x="%d" y="%d" font-size="18" font-weight="700" fill="%s">%s</text>`, pad, y+i*23, c.p.text, esc(line))
	}
	return y + (len(lines)-1)*23
}

func (c *canvas) chips(v view, y int) int {
	x := pad
	w := chipWidth(v.state.label)
	col := c.p.tones[v.state.tone]
	c.f(`<rect x="%d" y="%d" width="%d" height="24" rx="12" fill="%s" fill-opacity="0.14" stroke="%s"/>`, x, y, w, col, col)
	c.f(`<circle cx="%d" cy="%d" r="4" fill="%s"/>`, x+13, y+12, col)
	c.f(`<text x="%d" y="%d" font-size="12" font-weight="600" fill="%s">%s</text>`, x+23, y+16, c.p.text, esc(v.state.label))
	x += w + 8
	if v.released {
		label := v.dayText + " · " + v.finish.Label
		w := chipWidth(label)
		fc := c.finishColour(v.finish)
		c.f(`<rect x="%d" y="%d" width="%d" height="24" rx="12" fill="%s" stroke="%s"/>`, x, y, w, c.p.selected, fc)
		c.f(`<circle cx="%d" cy="%d" r="4" fill="%s"/>`, x+13, y+12, fc)
		c.f(`<text x="%d" y="%d" font-size="12" font-weight="600" fill="%s">%s</text>`, x+23, y+16, c.p.text, esc(label))
	}
	return y + 24
}

func chipWidth(label string) int { return 32 + len([]rune(label))*7 }

func (c *canvas) main(v view, y int) int {
	cx, cy, r := pad+44, y+48, 40
	c.f(`<circle cx="%d" cy="%d" r="%d" fill="none" stroke="%s" stroke-width="8"/>`, cx, cy, r, c.p.track)
	if v.cost.arc {
		col := c.p.accent
		if v.cost.over {
			col = c.p.tones[toneDanger]
		}
		c.f(`<circle cx="%d" cy="%d" r="%d" fill="none" stroke="%s" stroke-width="8" stroke-linecap="round" pathLength="100" stroke-dasharray="%s 100" transform="rotate(-90 %d %d)"/>`,
			cx, cy, r, col, strconv.FormatFloat(v.cost.share*100, 'f', 1, 64), cx, cy)
	}
	size := 13
	if len([]rune(v.cost.value)) > 10 {
		size = 10
	}
	c.f(`<text x="%d" y="%d" font-size="%d" font-weight="700" text-anchor="middle" fill="%s">%s</text>`, cx, cy+2, size, c.p.text, esc(v.cost.value))
	if v.cost.caption != "" {
		c.f(`<text x="%d" y="%d" font-size="9" text-anchor="middle" fill="%s">%s</text>`, cx, cy+15, c.p.muted, esc(fit(v.cost.caption, 14)))
	}
	tx, tw := pad+104, Width-pad-(pad+104)
	c.tile(tx, y, tw, "DIFF")
	if v.diffKnown {
		c.f(`<text x="%d" y="%d" font-size="13" font-weight="700"><tspan fill="%s">%s</tspan> <tspan fill="%s">%s</tspan>`, tx+10, y+35, c.p.tones[toneSuccess], esc(v.adds), c.p.tones[toneDanger], esc(v.dels))
		if v.files != "" {
			c.f(` <tspan font-weight="400" fill="%s">%s</tspan>`, c.p.muted, esc(v.files))
		}
		c.f(`</text>`)
	} else {
		c.f(`<text x="%d" y="%d" font-size="12" fill="%s">%s</text>`, tx+10, y+35, c.p.muted, esc(v.diff))
	}
	c.tile(tx, y+52, tw, "PR · CI")
	if v.pr == "" {
		c.f(`<text x="%d" y="%d" font-size="12" fill="%s">No pull request yet</text>`, tx+10, y+87, c.p.muted)
	} else {
		c.f(`<text x="%d" y="%d" font-size="13" font-weight="700" fill="%s">%s <tspan font-size="12" fill="%s">%s</tspan> <tspan font-size="12" font-weight="600" fill="%s">· %s</tspan></text>`,
			tx+10, y+87, c.p.accentFg, esc(v.pr), c.p.tones[v.prState.tone], esc(v.prState.label), c.p.tones[v.ci.tone], esc(v.ci.label))
	}
	return y + 96
}

func (c *canvas) tile(x, y, w int, label string) {
	c.f(`<rect x="%d" y="%d" width="%d" height="44" rx="8" fill="%s" fill-opacity="0.5" stroke="%s"/>`, x, y, w, c.p.selected, c.p.border)
	c.f(`<text x="%d" y="%d" font-size="9" font-weight="700" letter-spacing="0.8" fill="%s">%s</text>`, x+10, y+16, c.p.muted, esc(label))
}

func (c *canvas) slab(v view, y int) int {
	g := v.grade
	c.f(`<rect x="%d" y="%d" width="%d" height="56" rx="8" fill="%s" stroke="%s" stroke-width="1.5" data-slot="grade"/>`, pad, y, Width-2*pad, c.p.selected, c.p.accent)
	c.f(`<text x="%d" y="%d" font-size="9" font-weight="700" letter-spacing="0.8" fill="%s">GRADE</text>`, pad+12, y+18, c.p.muted)
	c.f(`<text x="%d" y="%d" font-size="26" font-weight="800" fill="%s">%s</text>`, pad+12, y+46, c.p.text, esc(g.overall))
	status := "Settled"
	if g.provisional {
		status = "Provisional"
	}
	if g.label != "" {
		status = g.label
	}
	c.f(`<text x="%d" y="%d" font-size="12" font-weight="700" fill="%s">%s</text>`, pad+80, y+25, c.p.text, esc(status))
	c.f(`<text x="%d" y="%d" font-size="10" fill="%s">Formula %s</text>`, pad+80, y+41, c.p.muted, esc(g.formula))
	if g.qualifiers != "" {
		c.f(`<text x="%d" y="%d" font-size="12" font-weight="700" letter-spacing="1" text-anchor="end" fill="%s">%s</text>`, Width-pad-12, y+33, c.p.tones[toneAttention], esc(fit(g.qualifiers, 16)))
	}
	return y + 56
}

func (c *canvas) conditionRow(v view, y int) int {
	col := c.p.tones[toneDanger]
	label := "Cracked · " + plural(int64(v.cracks), "crack")
	if v.mended > 0 {
		label += " · " + count(int64(v.mended)) + " mended"
	}
	if v.condition == "mended" {
		col = c.p.gold
		label = "Mended · " + plural(int64(v.cracks), "crack") + " sealed in gold"
	}
	c.f(`<g data-slot="condition"><path d="M%d %d l5 -6 l3 4 l5 -8" fill="none" stroke="%s" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`, pad, y+16, col)
	c.f(`<text x="%d" y="%d" font-size="12" font-weight="600" fill="%s">%s</text></g>`, pad+22, y+15, c.p.text, esc(label))
	return y + 20
}

func (c *canvas) cracks(v view) {
	n := min(v.cracks, 3)
	for i := range n {
		col := c.p.tones[toneDanger]
		if i < v.mended {
			col = c.p.gold
		}
		c.f(`<path d="M%d %d l-6 14 l8 10 l-7 14 l6 12 l-5 14" fill="none" stroke="%s" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" data-crack="%d"/>`,
			Width-10, 64+i*72, col, i+1)
	}
}

func (c *canvas) crewRow(v view, y int) int {
	c.f(`<text x="%d" y="%d" font-size="9" font-weight="700" letter-spacing="0.8" fill="%s">CREW</text>`, pad, y+12, c.p.muted)
	c.f(`<text x="%d" y="%d" font-size="12" fill="%s">%s</text>`, pad+44, y+12, c.p.text, esc(fit(v.crew, 42)))
	return y + 16
}

func (c *canvas) stewardRow(v view, y int) int {
	cy := y + 20
	c.f(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`, pad, y, Width-pad, y, c.p.border)
	if v.steward == "" {
		c.f(`<circle cx="%d" cy="%d" r="16" fill="none" stroke="%s" stroke-dasharray="3 3"/>`, pad+16, cy+4, c.p.border)
		c.f(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`, pad+42, cy+2, pad+180, cy+2, c.p.border)
		c.f(`<text x="%d" y="%d" font-size="10" fill="%s">Unsigned · no merge or approval yet</text>`, pad+42, cy+16, c.p.muted)
		return y + 44
	}
	c.f(`<circle cx="%d" cy="%d" r="16" fill="%s"/>`, pad+16, cy+4, c.p.selected)
	c.f(`<text x="%d" y="%d" font-size="12" font-weight="700" text-anchor="middle" fill="%s">%s</text>`, pad+16, cy+8, c.p.accentFg, esc(initials(v.steward)))
	c.f(`<text x="%d" y="%d" font-size="14" font-weight="700" font-style="italic" fill="%s">%s</text>`, pad+42, cy+2, c.p.text, esc(fit(v.steward, 30)))
	detail := "Steward"
	if v.stewardBy != "" {
		detail += " · " + v.stewardBy
	}
	c.f(`<text x="%d" y="%d" font-size="10" fill="%s">%s</text>`, pad+42, cy+16, c.p.muted, esc(detail))
	return y + 44
}

func (c *canvas) footer(v view, height int) {
	y := height - 18
	c.f(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`, pad, y-18, Width-pad, y-18, c.p.border)
	c.f(`<text x="%d" y="%d" font-size="11" fill="%s">%s</text>`, pad, y, c.p.muted, esc(v.plays))
	c.f(`<text x="%d" y="%d" font-size="11" text-anchor="end" fill="%s">%s</text>`, Width-pad, y, c.p.muted, esc(fit(strings.Join(v.ids, " · "), 40)))
}

func describe(v view) string {
	parts := []string{v.state.label, "Cost " + v.cost.text, "Diff " + v.diff}
	if v.pr != "" {
		parts = append(parts, "Pull request "+v.pr+" "+v.prState.label+", "+v.ci.label)
	}
	if v.released {
		parts = append(parts, v.dayText+" live, "+v.finish.Label+" finish")
	}
	if v.grade != nil {
		parts = append(parts, "Grade "+v.grade.overall)
	}
	if v.cracks > 0 {
		parts = append(parts, plural(int64(v.cracks), "crack")+", "+count(int64(v.mended))+" mended")
	}
	if v.steward != "" {
		parts = append(parts, "Steward "+v.steward)
	}
	return strings.Join(parts, ". ") + "."
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(clean(s)))
	return b.String()
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), r == '\u2028', r == '\u2029', r == '\uFEFF':
			return -1
		case unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, s)
}

func fit(s string, limit int) string {
	r := []rune(strings.TrimSpace(clean(s)))
	if len(r) <= limit {
		return string(r)
	}
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}

func wrap(s string, width, lines int) []string {
	words := strings.Fields(clean(s))
	var out []string
	cur := ""
	for i := 0; i < len(words); i++ {
		w := words[i]
		for len([]rune(w)) > width {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			r := []rune(w)
			out = append(out, string(r[:width]))
			w = string(r[width:])
		}
		switch {
		case cur == "":
			cur = w
		case len([]rune(cur))+1+len([]rune(w)) <= width:
			cur += " " + w
		default:
			out = append(out, cur)
			cur = w
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		return []string{""}
	}
	if len(out) > lines {
		last := strings.Join(out[lines-1:], " ")
		out = append(out[:lines-1], fit(last, width))
	}
	return out
}

func initials(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b []rune
	for _, p := range parts {
		b = append(b, []rune(p)[0])
		if len(b) == 2 {
			break
		}
	}
	return strings.ToUpper(string(b))
}

package clickup

import (
	"maps"
	"regexp"
	"strings"
)

// richBlock is one run of ClickUp's comment rich text: a piece of text and
// the formatting it carries. A line's own formatting (a list item, a code
// block) sits on the "\n" block that ends it.
type richBlock struct {
	Text       string         `json:"text"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// inlineMarkup matches the Markdown Ploeg writes into tracker comments, in
// this group order: **bold**, `code`, [text](url), <sub>small print</sub>
// and a bare URL.
var inlineMarkup = regexp.MustCompile("\\*\\*(.+?)\\*\\*|`([^`]+)`|\\[([^\\]]+)\\]\\((https?://[^)\\s]+)\\)|<sub>(.*?)</sub>|(https?://[^\\s<>()\\[\\]]+)")

var (
	orderedItem = regexp.MustCompile(`^\d+\. `)
	lineBreak   = regexp.MustCompile(`(?i)<br\s*/?>`)
)

// richComment turns a Markdown comment into ClickUp's rich-text blocks and
// the same text with its markup removed. ClickUp shows comment_text
// verbatim, so without this a person reads the asterisks instead of bold.
// What it does not recognise stays as written.
func richComment(markdown string) (string, []richBlock) {
	var blocks []richBlock
	inFence := false
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n"), "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		var lineAttrs map[string]any
		switch {
		case inFence:
			blocks = append(blocks, richBlock{Text: line})
			lineAttrs = map[string]any{"code-block": map[string]any{"code-block": "plain"}}
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			blocks = append(blocks, inline(line[2:], nil)...)
			lineAttrs = map[string]any{"list": map[string]any{"list": "bullet"}}
		case orderedItem.MatchString(line):
			blocks = append(blocks, inline(orderedItem.ReplaceAllString(line, ""), nil)...)
			lineAttrs = map[string]any{"list": map[string]any{"list": "ordered"}}
		default:
			blocks = append(blocks, inline(lineBreak.ReplaceAllString(line, " "), nil)...)
		}
		blocks = append(blocks, richBlock{Text: "\n", Attributes: lineAttrs})
	}
	blocks = merge(blocks)
	var plain strings.Builder
	for _, b := range blocks {
		plain.WriteString(b.Text)
	}
	return strings.TrimRight(plain.String(), "\n"), blocks
}

func inline(text string, base map[string]any) []richBlock {
	var out []richBlock
	for text != "" {
		m := inlineMarkup.FindStringSubmatchIndex(text)
		if m == nil {
			out = append(out, richBlock{Text: text, Attributes: base})
			break
		}
		if m[0] > 0 {
			out = append(out, richBlock{Text: text[:m[0]], Attributes: base})
		}
		group := func(i int) string { return text[m[2*i]:m[2*i+1]] }
		has := func(i int) bool { return m[2*i] >= 0 }
		switch {
		case has(1):
			out = append(out, inline(group(1), with(base, "bold", true))...)
		case has(2):
			out = append(out, richBlock{Text: group(2), Attributes: with(base, "code", true)})
		case has(3):
			out = append(out, richBlock{Text: group(3), Attributes: with(base, "link", group(4))})
		case has(5):
			out = append(out, inline(group(5), with(base, "italic", true))...)
		case has(6):
			link := strings.TrimRight(group(6), ".,;:!?")
			out = append(out, richBlock{Text: link, Attributes: with(base, "link", link)})
			if rest := group(6)[len(link):]; rest != "" {
				out = append(out, richBlock{Text: rest, Attributes: base})
			}
		}
		text = text[m[1]:]
	}
	return out
}

func with(base map[string]any, key string, value any) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = value
	return out
}

// merge joins neighbouring unformatted blocks, so a plain comment is one
// block per line rather than one per piece the parser saw.
func merge(blocks []richBlock) []richBlock {
	out := make([]richBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Text == "" {
			continue
		}
		if n := len(out); n > 0 && len(b.Attributes) == 0 && len(out[n-1].Attributes) == 0 && b.Text != "\n" && out[n-1].Text != "\n" {
			out[n-1].Text += b.Text
			continue
		}
		out = append(out, b)
	}
	return out
}

package worker

import (
	"fmt"
	"html"
	"unicode"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

//go:generate go run ./internal/emojigen -out emoji_tables.go

const maxReportedHiddenCharacters = 20

const (
	zeroWidthJoiner   = 0x200D
	textPresentation  = 0xFE0E
	emojiPresentation = 0xFE0F
	firstUnicodeTag   = 0xE0000
	lastUnicodeTag    = 0xE007F
	firstMongolianFVS = 0x180B
	lastMongolianFVS  = 0x180F
	mongolianVowelSep = 0x180E
)

type hiddenCharacter struct {
	Path   string
	Line   int
	Column int
	Rune   rune
}

func (h hiddenCharacter) String() string {
	return fmt.Sprintf("%s line %d column %d (U+%04X)", h.Path, h.Line, h.Column, h.Rune)
}

type hiddenCharacters struct {
	Found []hiddenCharacter
	Total int
}

func (h *hiddenCharacters) record(c hiddenCharacter) {
	h.Total++
	if len(h.Found) < maxReportedHiddenCharacters {
		h.Found = append(h.Found, c)
	}
}

func isHiddenRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		unicode.Is(unicode.Variation_Selector, r) ||
		(r >= firstUnicodeTag && r <= lastUnicodeTag)
}

func isEmoji(r rune) bool { return unicode.Is(extendedPictographic, r) }

func isKeycapBase(r rune) bool { return (r >= '0' && r <= '9') || r == '#' || r == '*' }

func isMongolianLetter(r rune) bool { return unicode.Is(unicode.Mongolian, r) && unicode.IsLetter(r) }

func isMongolianFreeVariationSelector(r rune) bool {
	return r >= firstMongolianFVS && r <= lastMongolianFVS && r != mongolianVowelSep
}

type runeCursor struct {
	source        string
	line, column  int
	previous      rune
	afterEmoji    bool
	pendingJoiner *hiddenCharacter
}

func newRuneCursor(source string) runeCursor { return runeCursor{source: source, line: 1} }

func (c *runeCursor) here(r rune) hiddenCharacter {
	return hiddenCharacter{Path: c.source, Line: c.line, Column: c.column, Rune: r}
}

func (c *runeCursor) step(r rune, hidden *hiddenCharacters) {
	c.column++
	if c.pendingJoiner != nil {
		if !isEmoji(r) {
			hidden.record(*c.pendingJoiner)
		}
		c.pendingJoiner = nil
	}
	emoji := false
	switch {
	case r == zeroWidthJoiner && c.afterEmoji:
		joiner := c.here(r)
		c.pendingJoiner = &joiner
	case c.acceptsSelector(r):
		emoji = r == emojiPresentation && isEmoji(c.previous)
	case isHiddenRune(r):
		hidden.record(c.here(r))
	default:
		emoji = isEmoji(r) || (unicode.Is(emojiModifier, r) && c.afterEmoji)
	}
	c.previous, c.afterEmoji = r, emoji
	if r == '\n' {
		c.line, c.column = c.line+1, 0
	}
}

func (c *runeCursor) acceptsSelector(r rune) bool {
	switch {
	case r == textPresentation || r == emojiPresentation:
		return isEmoji(c.previous) || isKeycapBase(c.previous)
	case isMongolianFreeVariationSelector(r):
		return isMongolianLetter(c.previous)
	}
	return false
}

func (c *runeCursor) finish(hidden *hiddenCharacters) {
	if c.pendingJoiner != nil {
		hidden.record(*c.pendingJoiner)
		c.pendingJoiner = nil
	}
}

func scanWorkItemText(item work.WorkItem) hiddenCharacters {
	var hidden hiddenCharacters
	for _, field := range []struct{ source, text string }{
		{"Work Item title", item.Title},
		{"Work Item description", item.Description},
	} {
		cursor := newRuneCursor(field.source)
		for _, r := range html.UnescapeString(field.text) {
			cursor.step(r, &hidden)
		}
		cursor.finish(&hidden)
	}
	return hidden
}

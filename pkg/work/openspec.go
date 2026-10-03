package work

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// MaxOpenSpecChangeLen bounds an OpenSpec change identifier named by a Work Item.
const MaxOpenSpecChangeLen = 100

var (
	openSpecDirective  = regexp.MustCompile(`(?i)^openspec\s*:\s*(.*)$`)
	openSpecChangeID   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	htmlLineBreak      = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|h[1-6]|pre|blockquote|tr)>`)
	htmlTag            = regexp.MustCompile(`<[^>]*>`)
	openSpecIdentQuote = "`*_"
)

// OpenSpecChange returns the OpenSpec change a Work Item description names on
// a line "openspec: <change-id>", or "" when it names none. HTML markup is
// ignored, the key is case-insensitive and the identifier may be quoted in
// backticks. A directive whose identifier is not kebab-case, or two
// directives naming different changes, is an error.
func OpenSpecChange(description string) (string, error) {
	text := htmlLineBreak.ReplaceAllString(description, "\n")
	text = html.UnescapeString(htmlTag.ReplaceAllString(text, ""))
	var found string
	for _, line := range strings.Split(text, "\n") {
		m := openSpecDirective.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		id := strings.Trim(strings.TrimSpace(m[1]), openSpecIdentQuote)
		if len(id) > MaxOpenSpecChangeLen || !openSpecChangeID.MatchString(id) {
			return "", fmt.Errorf("the Work Item's openspec directive %q does not name a kebab-case change id of at most %d characters", strings.TrimSpace(line), MaxOpenSpecChangeLen)
		}
		if found != "" && found != id {
			return "", fmt.Errorf("the Work Item names two OpenSpec changes, %q and %q; name one", found, id)
		}
		found = id
	}
	return found, nil
}

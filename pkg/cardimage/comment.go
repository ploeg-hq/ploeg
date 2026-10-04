package cardimage

import (
	"regexp"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// CommentMarker opens the one card comment on a pull request. Ploeg finds
// the comment by it and edits it in place, so a lost comment id never posts
// a second card (ADR-0055). IsCardComment also recognises the marker of an
// earlier release, which named its product before ":run-card".
const CommentMarker = "<!-- ploeg:run-card -->"

var anyCardMarker = regexp.MustCompile(`^<!--\s*[a-z0-9-]+:run-card\s*-->`)

// IsCardComment reports whether a comment body is a Run card comment.
func IsCardComment(body string) bool {
	return anyCardMarker.MatchString(strings.TrimSpace(body))
}

// FileName is the name the card image is stored under on the forge.
func FileName(card store.OperatorCard) string {
	return "run-card-" + strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, card.WorkItemID) + ".svg"
}

// CommentBody is the card comment: the marker, a heading naming the moment,
// the card image when imageURL is an http(s) or root-relative URL, the
// Summary table and a footer that says who posted it and what it shows.
func CommentBody(card store.OperatorCard, now time.Time, headline, imageURL string) string {
	var b strings.Builder
	b.WriteString(CommentMarker + "\n### Run card · " + md(headline) + "\n\n")
	if dest, ok := imageDestination(imageURL); ok {
		b.WriteString("![Run card: " + md(fit(newView(card, now).title, 120)) + "](" + dest + ")\n\n")
	}
	b.WriteString(Summary(card, now))
	b.WriteString("\n<sub>Posted by Ploeg when this card reached a moment: a merge, a release to production, a new finish or a mend. " +
		"It shows the change's own facts and its steward; nothing on it ranks or scores a person.</sub>\n")
	return b.String()
}

func imageDestination(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" || strings.ContainsAny(u, " \t\r\n<>\"'()\\`") {
		return "", false
	}
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
	case strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"):
	default:
		return "", false
	}
	return u, true
}

package harness

import (
	"regexp"
	"strings"
)

const maxErrorLine = 300

var (
	errorWord      = regexp.MustCompile(`(?i)error|exception|traceback|invalid|refused|failed`)
	evidenceLabels = []string{"last agent message:", "stderr:"}
)

// ErrorLine picks the line of a harness's failure text a person should read
// first: the first line or " | "-separated part that names an error, else the
// last non-empty one, without the label the adapter put before it and cut to
// a few hundred characters.
func ErrorLine(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		for _, part := range strings.Split(line, " | ") {
			part = strings.TrimSpace(part)
			for _, label := range evidenceLabels {
				part = strings.TrimSpace(strings.TrimPrefix(part, label))
			}
			if part != "" {
				parts = append(parts, part)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	pick := parts[len(parts)-1]
	for _, p := range parts {
		if errorWord.MatchString(p) {
			pick = p
			break
		}
	}
	pick = strings.Join(strings.Fields(pick), " ")
	if runes := []rune(pick); len(runes) > maxErrorLine {
		pick = string(runes[:maxErrorLine]) + "…"
	}
	return pick
}

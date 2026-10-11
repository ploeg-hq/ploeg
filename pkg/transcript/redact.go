package transcript

import (
	"regexp"
	"strings"
)

// Redacted replaces every secret a transcript entry carried.
const Redacted = "[redacted]"

// minimumSecretLength keeps short values, which match ordinary words, from
// being masked as exact secrets.
const minimumSecretLength = 8

var secretPatterns = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), Redacted},
	{regexp.MustCompile(`\b[0-9a-f]{48}\b`), Redacted},
	{regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{8,}|Bearer [A-Za-z0-9._~+/=-]{8,})`), Redacted},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})`), Redacted},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), Redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), Redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), Redacted},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "${1}" + Redacted + "@"},
	{regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"',;]{8,}`), "${1}" + Redacted},
}

// Redactor masks the exact secrets a Run holds and the shapes of well-known
// credentials.
type Redactor struct {
	secrets []string
}

// NewRedactor masks each of secrets that is at least eight bytes long, in
// addition to the patterns every Redactor masks.
func NewRedactor(secrets ...string) Redactor {
	var r Redactor
	for _, s := range secrets {
		if len(s) >= minimumSecretLength {
			r.secrets = append(r.secrets, s)
		}
	}
	return r
}

// String returns s with every secret replaced by Redacted.
func (r Redactor) String(s string) string {
	if s == "" {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, Redacted)
	}
	for _, p := range secretPatterns {
		s = p.pattern.ReplaceAllString(s, p.replace)
	}
	return s
}

// Entry returns e with every free-text field redacted.
func (r Redactor) Entry(e Entry) Entry {
	e.Text = r.String(e.Text)
	e.Title = r.String(e.Title)
	e.Input = r.String(e.Input)
	if len(e.Paths) > 0 {
		paths := make([]string, len(e.Paths))
		for i, p := range e.Paths {
			paths[i] = r.String(p)
		}
		e.Paths = paths
	}
	if len(e.Plan) > 0 {
		steps := make([]Step, len(e.Plan))
		for i, s := range e.Plan {
			steps[i] = Step{Content: r.String(s.Content), Status: s.Status}
		}
		e.Plan = steps
	}
	return e
}

package transcript

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Redacted replaces every secret a transcript entry carried.
const Redacted = "[redacted]"

// minimumSecretLength keeps short values, which match ordinary words, from
// being masked as exact secrets.
const minimumSecretLength = 8

const redactionOverlap = 512

const regexpSpace = "\t\n\f\r "

type secretPattern struct {
	pattern *regexp.Regexp
	replace string
	present func(s, folded string) bool
}

var secretPatterns = []secretPattern{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), Redacted, contains("-----BEGIN ")},
	{regexp.MustCompile(`\b[0-9a-f]{48}\b`), Redacted, hasLowerHexRun(48)},
	{regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{8,}|Bearer [A-Za-z0-9._~+/=-]{8,})`), Redacted, containsFolded("SK-", "BEARER ")},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,})`), Redacted, contains("ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-")},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), Redacted, contains("AKIA", "ASIA")},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), Redacted, contains("xox")},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), Redacted, contains("eyJ")},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "${1}" + Redacted + "@", containsAll("://", "@")},
	{regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"',;]{8,}`), "${1}" + Redacted,
		either(containsFolded("PASSW", "SECRET", "TOKEN"), keyNamedFolded("API", "ACCESS", "PRIVATE"))},
	{regexp.MustCompile(`(?i)(auth\s*=\s*["']?)[^\s"',;]{8,}`), "${1}" + Redacted, assignsFolded("AUTH")},
	{regexp.MustCompile(`\b[spr]k_(?:live|test)_[A-Za-z0-9]{10,}`), Redacted, contains("k_live_", "k_test_")},
	{regexp.MustCompile(`("private_key_id"\s*:\s*")[0-9a-f]{40}`), "${1}" + Redacted, contains(`"private_key_id"`)},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), Redacted, contains("AIza")},
	{regexp.MustCompile(`(?i)((?:AccountKey|SharedAccessKey|SharedAccessSignature)\s*=\s*)[^;\s"']{8,}`), "${1}" + Redacted, containsFolded("ACCOUNTKEY", "SHAREDACCESS")},
	{regexp.MustCompile(`(?i)([?&;]sig=)[^&;\s"']{8,}`), "${1}" + Redacted, containsFolded("SIG=")},
}

func contains(literals ...string) func(s, folded string) bool {
	return func(s, _ string) bool { return containsAny(s, literals) }
}

func containsAll(literals ...string) func(s, folded string) bool {
	return func(s, _ string) bool {
		for _, l := range literals {
			if !strings.Contains(s, l) {
				return false
			}
		}
		return true
	}
}

func containsFolded(literals ...string) func(s, folded string) bool {
	return func(_, folded string) bool { return containsAny(folded, literals) }
}

func assignsFolded(key string) func(s, folded string) bool {
	return func(_, folded string) bool {
		for rest := folded; ; {
			i := strings.Index(rest, key)
			if i < 0 {
				return false
			}
			rest = rest[i+len(key):]
			if strings.HasPrefix(strings.TrimLeft(rest, regexpSpace), "=") {
				return true
			}
		}
	}
}

func either(a, b func(s, folded string) bool) func(s, folded string) bool {
	return func(s, folded string) bool { return a(s, folded) || b(s, folded) }
}

func keyNamedFolded(prefixes ...string) func(s, folded string) bool {
	return func(_, folded string) bool {
		for rest := folded; ; {
			i := strings.Index(rest, "KEY")
			if i < 0 {
				return false
			}
			before := strings.TrimRight(rest[:i], "_-")
			for _, p := range prefixes {
				if strings.HasSuffix(before, p) && len(rest[:i])-len(before) <= 1 {
					return true
				}
			}
			rest = rest[i+len("KEY"):]
		}
	}
}

func containsAny(s string, literals []string) bool {
	for _, l := range literals {
		if strings.Contains(s, l) {
			return true
		}
	}
	return false
}

func hasLowerHexRun(n int) func(s, folded string) bool {
	return func(s, _ string) bool {
		run := 0
		for i := 0; i < len(s); i++ {
			c := s[i]
			if '0' <= c && c <= '9' || 'a' <= c && c <= 'f' {
				run++
				if run >= n {
					return true
				}
			} else {
				run = 0
			}
		}
		return false
	}
}

func foldCase(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return strings.Map(smallestFold, s)
		}
	}
	return strings.ToUpper(s)
}

func smallestFold(r rune) rune {
	smallest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		smallest = min(smallest, f)
	}
	return smallest
}

// Redactor masks the exact secrets a Run holds and the shapes of well-known
// credentials.
type Redactor struct {
	secrets      []string
	spacedSecret bool
}

// NewRedactor masks each of secrets that is at least eight bytes long, in
// addition to the patterns every Redactor masks.
func NewRedactor(secrets ...string) Redactor {
	var r Redactor
	for _, s := range secrets {
		if len(s) >= minimumSecretLength {
			r.secrets = append(r.secrets, s)
			r.spacedSecret = r.spacedSecret || strings.ContainsAny(s, regexpSpace)
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
	folded := foldCase(s)
	for _, p := range secretPatterns {
		if p.present(s, folded) {
			s = p.pattern.ReplaceAllString(s, p.replace)
		}
	}
	return s
}

// Seal returns e as a transcript stores it: every free-text field redacted,
// then clamped to its limit. A field longer than its limit is cut at the last
// whitespace within redactionOverlap bytes past the limit before it is
// redacted, so bytes Clamp would discard are not scanned and a secret that
// straddles the cut is dropped whole. When no such whitespace exists the
// whole field is redacted first.
func (r Redactor) Seal(e Entry) Entry {
	var textCut, inputCut bool
	e.Text, textCut = r.head(e.Text, MaxTextBytes)
	e.Input, inputCut = r.head(e.Input, MaxInputBytes)
	e.Title, _ = r.head(e.Title, MaxTitleBytes)
	if len(e.Paths) > 0 {
		paths := make([]string, min(len(e.Paths), MaxPaths))
		for i := range paths {
			paths[i], _ = r.head(e.Paths[i], MaxPathBytes)
		}
		e.Paths = paths
	}
	if len(e.Plan) > 0 {
		steps := make([]Step, min(len(e.Plan), MaxPlanSteps))
		for i := range steps {
			content, _ := r.head(e.Plan[i].Content, MaxStepBytes)
			steps[i] = Step{Content: content, Status: e.Plan[i].Status}
		}
		e.Plan = steps
	}
	e = Clamp(e)
	e.Clipped = e.Clipped || textCut || inputCut
	return e
}

func (r Redactor) head(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return r.String(s), false
	}
	if r.spacedSecret || len(s) <= limit+redactionOverlap {
		return r.String(s), true
	}
	window, _ := clip(s, limit+redactionOverlap)
	cut := strings.LastIndexAny(window, regexpSpace)
	if cut < limit {
		return r.String(s), true
	}
	return r.String(window[:cut]), true
}

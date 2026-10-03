package shiftengine

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// usageReportMarker identifies the one report comment on a pull request.
// Refresh is find-by-marker then edit, so a lost comment id can never cause a
// second copy (design D2).
const usageReportMarker = "<!-- ploeg:usage-report -->"

// reportLinkConfig is the two optional dashboard bases. The links section is
// omitted entirely when both are empty, and the report still renders.
type reportLinkConfig struct {
	// GrafanaURL is the Grafana base. The Unfold — Loop dashboard filtered by
	// team, the Run Explorer filtered by alias and the Spend & Attribution
	// dashboard all hang off it.
	GrafanaURL string
	// VloerURL is Vloer's base; the report links the Work Item page,
	// <VloerURL>/#work/<id>.
	VloerURL string
}

// usageReportInput is everything the report renders. It is read from state
// Ploeg already records; rendering is pure — no clock, no network, no store.
type usageReportInput struct {
	Shift    store.ShiftUsage
	Ledger   store.ShiftLedger
	TraceID  string // writing Run's alias ploeg-<12hex>; "" if none
	Evidence Evidence
	// Links carries the optional dashboard bases.
	Links reportLinkConfig
}

// Evidence is the writing Run's verification as stored; the zero value means
// the report renders "not recorded".
type Evidence struct {
	Result string // e.g. "passed", "failed (go test ./...)", "incomplete (...)", "unknown"
	Commit string // the verified commit; "" when absent
	Dirty  bool   // the working tree had uncommitted changes when the checks ran
}

// money formats a US dollar amount the way the Unfold — Loop dashboard does:
// "US$ 0,06" — a space after US$, and a decimal comma with a dot as the
// thousands separator (nl-NL). Hand-rolled rather than locale-dependent so
// rendering is pure and identical on every host.
func money(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	// Round half-up to cents, then split.
	cents := int64(v*100 + 0.5)
	whole := cents / 100
	frac := cents % 100
	s := groupThousands(strconv.FormatInt(whole, 10)) + "," + fmt.Sprintf("%02d", frac)
	if neg {
		s = "-" + s
	}
	return "US$ " + s
}

// tokenCount formats an integer with dot thousands separators (nl-NL).
func tokenCount(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := groupThousands(strconv.FormatInt(n, 10))
	if neg {
		s = "-" + s
	}
	return s
}

// groupThousands inserts a dot every three digits, left to right.
func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
	}
	for i := first; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// runModels joins a Run's models, or a dash when none were recorded.
func runModels(models []string) string {
	if len(models) == 0 {
		return "—"
	}
	sorted := append([]string(nil), models...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// verdict renders a reading Run's verdict; writers carry none.
func verdict(v string) string {
	switch v {
	case harness.VerdictApprove:
		return "approve"
	case harness.VerdictRequestChanges:
		return "request changes"
	}
	return "—"
}

// outcomeOrDash renders an Outcome, tolerating the cancelled Run's empty one.
func outcomeOrDash(o string) string {
	if strings.TrimSpace(o) == "" {
		return "cancelled"
	}
	return o
}

// runRow is one Run's line in the per-Run table.
func runRow(r store.RunUsage) string {
	cost := "unavailable"
	if r.HasUsage {
		cost = money(r.CostUSD)
		if !r.Settled() {
			cost += " (provisional)"
		}
	}
	tokens := "unavailable"
	if r.HasUsage {
		tokens = tokenCount(r.InputTokens) + " / " + tokenCount(r.OutputTokens)
		if !r.Settled() {
			tokens += " (provisional)"
		}
	}
	writer := "no"
	if r.Writes {
		writer = "yes"
	}
	return fmt.Sprintf("| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s |\n",
		r.Role, r.Round, writer, outcomeOrDash(r.Outcome), verdict(r.Verdict),
		runModels(r.Models), tokens, cost, duration(r.Duration), accountState(r))
}

// duration renders how long a Run ran, or a dash when it was not recorded.
func duration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return d.Round(time.Second).String()
}

// accountState names the managed account's state, or a dash for a Run that has
// none (a reading Run mints no account, ADR-0013).
func accountState(r store.RunUsage) string {
	if r.AccountState == "" {
		return "—"
	}
	return r.AccountState
}

// usageReport renders the one body. Pure: no clock, no network, no store.
func usageReport(in usageReportInput) string {
	u := in.Shift
	ledger := in.Ledger
	if ledger == (store.ShiftLedger{}) {
		ledger = u.Ledger
	}
	runs := u.Runs

	var b strings.Builder
	b.WriteString(usageReportMarker + "\n\n")
	b.WriteString("### Ploeg usage report\n\n")

	settled := true
	for _, r := range runs {
		if !r.Settled() {
			settled = false
			break
		}
	}
	if settled {
		b.WriteString("_Settled._\n\n")
	} else {
		b.WriteString("_Costs marked **provisional** until the settlement sweep reconciles each Run's inference account._\n\n")
	}

	b.WriteString("| Run | Round | Wrote | Outcome | Verdict | Models billed | Prompt / completion tokens | Cost | Duration | Account |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	if len(runs) == 0 {
		b.WriteString("| _no finished Runs_ |  |  |  |  |  |  |  |  |  |\n")
	}
	for _, r := range runs {
		b.WriteString(runRow(r))
	}

	b.WriteString("\n**Shift totals**\n\n")
	fmt.Fprintf(&b, "- Authorized: %s\n", money(ledger.Budget))
	fmt.Fprintf(&b, "- Spent: %s\n", money(ledger.Spent))
	fmt.Fprintf(&b, "- Reserved: %s\n", money(ledger.Reserved))
	fmt.Fprintf(&b, "- Remaining: %s\n", money(ledger.Remaining()))
	fmt.Fprintf(&b, "- Rounds used: %d\n", u.Round)
	if strings.TrimSpace(u.CloseReason) != "" {
		fmt.Fprintf(&b, "- Close reason: %s\n", u.CloseReason)
	}
	if in.TraceID != "" {
		fmt.Fprintf(&b, "- Trace alias: `%s`\n", in.TraceID)
	}

	b.WriteString("\n**Evidence**\n\n")
	b.WriteString(evidenceSection(in.Evidence))

	if links := linksSection(in.Links, u, in.TraceID); links != "" {
		b.WriteString("\n")
		b.WriteString(links)
	}

	b.WriteString("\n<sub>Posted by Ploeg and updated in place.</sub>\n")
	return b.String()
}

// evidenceSection states what Ploeg observed of the writing Run's
// verification. When none was recorded it says so plainly and names no commit.
func evidenceSection(ev Evidence) string {
	var b strings.Builder
	if strings.TrimSpace(ev.Result) == "" {
		b.WriteString("Verification: not recorded — Ploeg observed no check result for the writing Run.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Verification: %s\n", ev.Result)
	if ev.Commit != "" {
		fmt.Fprintf(&b, "\nCommit verified: `%s`\n", ev.Commit)
	}
	if ev.Dirty {
		b.WriteString("\nThe working tree had uncommitted changes when the checks ran, so the result may not match the pushed commit.\n")
	}
	b.WriteString("\nThe full verification output is in the writing Run's findings comment on this pull request.\n")
	return b.String()
}

// linksSection renders the dashboard links from configuration. It returns ""
// (omitting the section) when no base URL is configured.
func linksSection(links reportLinkConfig, u store.ShiftUsage, alias string) string {
	grafana := strings.TrimRight(links.GrafanaURL, "/")
	vloer := strings.TrimRight(links.VloerURL, "/")
	if grafana == "" && vloer == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Where to dig deeper**\n\n")
	if grafana != "" {
		loop := grafana + "/d/glide-loop"
		if team := strings.TrimSpace(u.Team); team != "" {
			loop += "?var-team=" + url.QueryEscape(team)
		}
		fmt.Fprintf(&b, "- [Unfold — Loop dashboard](%s)\n", loop)
		if alias != "" {
			fmt.Fprintf(&b, "- [Run Explorer](%s/d/dark-factory-run-explorer?var-run=%s)\n", grafana, url.QueryEscape(alias))
		}
		fmt.Fprintf(&b, "- [Spend & Attribution](%s/d/dark-factory-spend-attribution)\n", grafana)
	}
	if vloer != "" {
		if u.WorkItemID > 0 {
			fmt.Fprintf(&b, "- [This Work Item in Vloer](%s/#work/%d)\n", vloer, u.WorkItemID)
		} else {
			fmt.Fprintf(&b, "- [Work in Vloer](%s/#work)\n", vloer)
		}
	}
	return b.String()
}

// --- evidence parsing -------------------------------------------------------

var (
	// A summary marker the worker appends to a writing Run's Summary:
	// "[Ploeg verification passed]", "[Ploeg verification failed: <cmd>]" or
	// "[Ploeg verification incomplete: <reason>]".
	verificationSummaryRe = regexp.MustCompile(`\[Ploeg verification ([^\]]*)\]`)
	// The commit sentence the worker writes into the verification section.
	verificationCommitRe = regexp.MustCompile("commit `([0-9a-f]+)`")
)

// parseEvidence derives the writing Run's verification from the Shift's
// reports. It takes the LAST Run that wrote — the one whose commit is at the
// branch tip. Its structured Verification, when present, is the whole answer;
// agent-written prose cannot change it. Only a Run reported before the
// worker sent that record falls back to the prose the worker appended. It
// returns the zero Evidence when there is no writing Run or it carries no
// verification at all, which the report renders as "not recorded" rather
// than a blank that could read as a pass.
func parseEvidence(reports []store.RunReport) Evidence {
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if !r.Writes {
			continue
		}
		if r.Verification != nil {
			return structuredEvidence(*r.Verification)
		}
		return legacyEvidence(r)
	}
	return Evidence{}
}

func structuredEvidence(v harness.Verification) Evidence {
	ev := Evidence{Commit: v.Commit, Dirty: v.Dirty}
	switch v.Result {
	case harness.VerificationPassed:
		ev.Result = "passed"
	case harness.VerificationFailed:
		ev.Result = "failed"
		if c, ok := v.FailedCheck(); ok {
			ev.Result = "failed (" + c.Command + ")"
		}
	case harness.VerificationIncomplete:
		ev.Result = "incomplete"
		if strings.TrimSpace(v.Stopped) != "" {
			ev.Result = "incomplete (" + strings.TrimSpace(v.Stopped) + ")"
		}
	default:
		ev.Result = "unknown"
	}
	return ev
}

// legacyEvidence reads a Run reported by a worker that sent no structured
// record. The worker appended its marker to the end of the summary and its
// section to the end of the findings, so the last of each is its own.
func legacyEvidence(r store.RunReport) Evidence {
	at := strings.LastIndex(r.Findings, verificationHeading)
	if at < 0 {
		return Evidence{}
	}
	section := r.Findings[at:]
	ev := Evidence{Dirty: strings.Contains(section, "The working tree had uncommitted changes")}
	if m := verificationSummaryRe.FindAllStringSubmatch(r.Summary, -1); m != nil {
		ev.Result = describeVerification(m[len(m)-1][1])
	}
	if ev.Result == "" {
		ev.Result = "unknown"
	}
	if m := verificationCommitRe.FindStringSubmatch(section); m != nil {
		ev.Commit = m[1]
	}
	return ev
}

// verificationHeading is the marker the worker writes; it must match
// pkg/worker's verificationHeading byte for byte.
const verificationHeading = "### Ploeg verification"

// describeVerification turns the marker's tail into a readable result.
func describeVerification(tail string) string {
	tail = strings.TrimSpace(tail)
	switch {
	case tail == "passed":
		return "passed"
	case strings.HasPrefix(tail, "failed:"):
		return "failed (" + strings.TrimSpace(strings.TrimPrefix(tail, "failed:")) + ")"
	case strings.HasPrefix(tail, "incomplete:"):
		return "incomplete (" + strings.TrimSpace(strings.TrimPrefix(tail, "incomplete:")) + ")"
	}
	return tail
}

// traceAlias is the writing Run's alias, the identifier the Run Explorer
// filters on. It returns "" when no writing Run carries an account alias.
func traceAlias(runs []store.RunUsage) string {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Writes && runs[i].Alias != "" {
			return runs[i].Alias
		}
	}
	return ""
}

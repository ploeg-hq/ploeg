package shiftengine

import (
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func sampleRuns() []store.RunUsage {
	return []store.RunUsage{
		{Role: "analyst", Round: 1, Outcome: "no_change_needed", Verdict: "", Alias: "", HasUsage: true,
			Models: []string{"deepseek-flash"}, InputTokens: 1200, OutputTokens: 340, CostUSD: 0.04,
			Duration: 90 * time.Second},
		{Role: "builder", Round: 2, Writes: true, Outcome: "pr_opened", Alias: "ploeg-abcdef012345",
			HasUsage: true, Models: []string{"deepseek-flash", "gpt-oss-120b"}, InputTokens: 15004, OutputTokens: 2200,
			CostUSD: 0.02, AccountState: "reconciled", Duration: 5 * time.Minute},
	}
}

func TestUsageReportRendersPerRunAndTotals(t *testing.T) {
	body := usageReport(usageReportInput{
		Shift:    store.ShiftUsage{Team: "bronze", Round: 2, CloseReason: "plan_exhausted", Runs: sampleRuns()},
		Ledger:   store.ShiftLedger{Budget: 1, Spent: 0.06, Reserved: 0},
		TraceID:  "ploeg-abcdef012345",
		Evidence: Evidence{Result: "passed", Commit: "abcdef123456"},
	})

	for _, want := range []string{
		usageReportMarker,
		"### Ploeg usage report",
		"| analyst | 1 | no | no_change_needed | — | deepseek-flash | 1.200 / 340 | US$ 0,04 | 1m30s | — |",
		"| builder | 2 | yes | pr_opened | — | deepseek-flash, gpt-oss-120b | 15.004 / 2.200 | US$ 0,02 | 5m0s | reconciled |",
		"Authorized: US$ 1,00",
		"Spent: US$ 0,06",
		"Reserved: US$ 0,00",
		"Remaining: US$ 0,94",
		"Rounds used: 2",
		"Close reason: plan_exhausted",
		"Trace alias: `ploeg-abcdef012345`",
		"Verification: passed",
		"Commit verified: `abcdef123456`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "US$ 0.06") {
		t.Errorf("money did not render nl-NL:\n%s", body)
	}
	if !strings.Contains(body, usageReportMarker) {
		t.Errorf("report lost its marker")
	}
}

func TestUsageReportMarksUnreconciledProvisional(t *testing.T) {
	runs := []store.RunUsage{
		{Role: "builder", Round: 1, Writes: true, Outcome: "pr_opened", HasUsage: true,
			InputTokens: 100, OutputTokens: 20, CostUSD: 0.03, Alias: "ploeg-aaaaaaaaaaaa", AccountState: "blocked"},
	}
	body := usageReport(usageReportInput{Shift: store.ShiftUsage{Runs: runs}, Ledger: store.ShiftLedger{Budget: 1, Spent: 0.03}})
	if !strings.Contains(body, "provisional") {
		t.Errorf("an unreconciled Run was not marked provisional:\n%s", body)
	}
	if !strings.Contains(body, "US$ 0,03 (provisional)") {
		t.Errorf("cost did not carry the provisional mark:\n%s", body)
	}
	if !strings.Contains(body, "100 / 20 (provisional)") {
		t.Errorf("tokens did not carry the provisional mark:\n%s", body)
	}

	settled := usageReport(usageReportInput{Shift: store.ShiftUsage{Runs: []store.RunUsage{
		{Role: "builder", Round: 1, Writes: true, Outcome: "pr_opened", HasUsage: true, CostUSD: 0.03, AccountState: "reconciled"},
	}}})
	if strings.Contains(settled, "provisional") {
		t.Errorf("a reconciled Run was marked provisional:\n%s", settled)
	}
	if !strings.Contains(settled, "_Settled._") {
		t.Errorf("settled report does not say so:\n%s", settled)
	}
}

func TestUsageReportUnreadableSpendIsUnavailable(t *testing.T) {
	runs := []store.RunUsage{
		{Role: "builder", Round: 1, Writes: true, Outcome: "pr_opened", HasUsage: false, Authorized: 2, AccountState: "blocked"},
	}
	body := usageReport(usageReportInput{Shift: store.ShiftUsage{Runs: runs}})
	if strings.Count(body, "unavailable") < 2 {
		t.Errorf("unreadable spend was not marked unavailable for both tokens and cost:\n%s", body)
	}
	if strings.Contains(body, "US$ 2,00") {
		t.Errorf("the authorization was rendered as cost:\n%s", body)
	}
}

// A key-shaped string must never appear: the report prints the alias
// ploeg-<12hex> and nothing that looks like a secret.
func TestUsageReportPrintsAliasOnly(t *testing.T) {
	const secret = "sk-1234567890abcdef1234567890abcdef"
	runs := []store.RunUsage{
		{Role: "builder", Round: 1, Writes: true, Outcome: "pr_opened", HasUsage: true, CostUSD: 0.01,
			Alias: "ploeg-abcdef012345", AccountState: "reconciled"},
	}
	body := usageReport(usageReportInput{Shift: store.ShiftUsage{Runs: runs}, TraceID: "ploeg-abcdef012345"})
	if strings.Contains(body, secret) || strings.Contains(body, "gateway_key_id") || strings.Contains(body, "sk-") {
		t.Errorf("report carried a secret-shaped string:\n%s", body)
	}
	if !strings.Contains(body, "`ploeg-abcdef012345`") {
		t.Errorf("report lost the trace alias:\n%s", body)
	}
}

func TestUsageReportReadingRunHasNoAccountState(t *testing.T) {
	runs := []store.RunUsage{{Role: "analyst", Round: 1, Outcome: "no_change_needed", HasUsage: true, CostUSD: 0.01}}
	body := usageReport(usageReportInput{Shift: store.ShiftUsage{Runs: runs}})
	if !strings.Contains(body, "| analyst | 1 | no | no_change_needed | — |") {
		t.Errorf("reading Run row is wrong:\n%s", body)
	}
	if strings.Contains(body, "reserved|minting") || strings.Contains(body, "AccountState") {
		t.Errorf("the account state leaked into the rendered body:\n%s", body)
	}
}

// The evidence fixture is byte-identical to pkg/worker/verify_test.go's
// markdown() output for a passing run, so a wording change fails both.
func TestUsageReportEvidenceParsesVerificationAndCommit(t *testing.T) {
	const markdown = "### Ploeg verification\n\n" +
		"Ploeg ran the configured checks on commit `abcdef123456` after the agent finished.\n\n" +
		"| Check | Result |\n| --- | --- |\n| `go test ./...` | passed |\n"
	reports := []store.RunReport{{
		Role: "builder", Round: 2, Writes: true,
		Summary:  "opened [Ploeg verification passed]",
		Findings: markdown,
	}}
	ev := parseEvidence(reports)
	if ev.Result != "passed" || ev.Commit != "abcdef123456" {
		t.Fatalf("parseEvidence = %+v, want passed/abcdef123456", ev)
	}
	body := usageReport(usageReportInput{Shift: store.ShiftUsage{}, Evidence: ev})
	if !strings.Contains(body, "Verification: passed") || !strings.Contains(body, "`abcdef123456`") {
		t.Errorf("report did not state the verification and commit:\n%s", body)
	}
}

func TestParseEvidenceReadsFailedAndIncomplete(t *testing.T) {
	failed := parseEvidence([]store.RunReport{{Writes: true, Summary: "opened [Ploeg verification failed: go test ./...]",
		Findings: "### Ploeg verification\n\nPloeg ran the configured checks on commit `123abc456def` after the agent finished.\n"}})
	if failed.Result != "failed (go test ./...)" || failed.Commit != "123abc456def" {
		t.Errorf("failed evidence = %+v", failed)
	}
	incomplete := parseEvidence([]store.RunReport{{Writes: true, Summary: "opened [Ploeg verification incomplete: the Run was cancelled]",
		Findings: "### Ploeg verification\n"}})
	if !strings.HasPrefix(incomplete.Result, "incomplete (") {
		t.Errorf("incomplete evidence = %+v", incomplete)
	}
}

// The last WRITING Run wins: an earlier writer's verification is superseded by
// the fix round at the branch tip.
func TestParseEvidenceTakesTheLastWriter(t *testing.T) {
	reports := []store.RunReport{
		{Role: "builder", Round: 1, Writes: true, Summary: "[Ploeg verification passed]", Findings: "### Ploeg verification\n... commit `aaaa11112222` ..."},
		{Role: "reviewer", Round: 2, Writes: false, Summary: "looks fine", Findings: "### Ploeg verification\n"},
		{Role: "builder", Round: 3, Writes: true, Summary: "[Ploeg verification failed: gofmt]", Findings: "### Ploeg verification\n... commit `bbbb33334444` ..."},
	}
	ev := parseEvidence(reports)
	if ev.Commit != "bbbb33334444" || ev.Result != "failed (gofmt)" {
		t.Fatalf("parseEvidence = %+v, want the last writer bbbb33334444/failed", ev)
	}
}

func TestUsageReportEvidenceNotRecordedIsExplicit(t *testing.T) {
	for name, reports := range map[string][]store.RunReport{
		"no heading":     {{Role: "builder", Round: 1, Writes: true, Summary: "[Ploeg verification passed]", Findings: "just prose"}},
		"readers only":   {{Role: "analyst", Round: 1, Writes: false, Summary: "read it", Findings: "### Ploeg verification\n"}},
		"empty findings": {{Role: "builder", Round: 1, Writes: true, Summary: "[Ploeg verification passed]", Findings: ""}},
		"no reports":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			ev := parseEvidence(reports)
			if ev.Result != "" || ev.Commit != "" {
				t.Fatalf("expected zero evidence, got %+v", ev)
			}
			body := usageReport(usageReportInput{Shift: store.ShiftUsage{}, Evidence: ev})
			if !strings.Contains(body, "Verification: not recorded") {
				t.Errorf("report did not say the verification was not recorded:\n%s", body)
			}
			if strings.Contains(body, "Commit verified") {
				t.Errorf("report named a commit for an unrecorded verification:\n%s", body)
			}
		})
	}
}

func TestUsageReportLinksFromConfiguration(t *testing.T) {
	u := store.ShiftUsage{WorkItemID: 138, Team: "bronze", Runs: []store.RunUsage{{Role: "builder", Round: 1, Writes: true, AccountState: "reconciled"}}}
	withLinks := usageReport(usageReportInput{
		Shift: u, TraceID: "ploeg-abcdef012345",
		Links: reportLinkConfig{GrafanaURL: "https://grafana.example/", VloerURL: "https://vloer.example/"},
	})
	for _, want := range []string{
		"**Where to dig deeper**",
		"(https://grafana.example/d/glide-loop?var-team=bronze)",
		"(https://grafana.example/d/dark-factory-run-explorer?var-run=ploeg-abcdef012345)",
		"(https://grafana.example/d/dark-factory-spend-attribution)",
		"[This Work Item in Vloer](https://vloer.example/#work/138)",
	} {
		if !strings.Contains(withLinks, want) {
			t.Errorf("report missing link %q:\n%s", want, withLinks)
		}
	}

	noLinks := usageReport(usageReportInput{Shift: u})
	if strings.Contains(noLinks, "Where to dig deeper") || strings.Contains(noLinks, "http") {
		t.Errorf("links rendered with no base URL configured:\n%s", noLinks)
	}
	if !strings.Contains(noLinks, "### Ploeg usage report") {
		t.Errorf("report stopped rendering when links were omitted:\n%s", noLinks)
	}
}

func TestLinksSection(t *testing.T) {
	const grafana, vloer = "https://grafana.example", "https://vloer.example"
	cases := []struct {
		name    string
		links   reportLinkConfig
		usage   store.ShiftUsage
		alias   string
		want    []string
		notWant []string
	}{
		{
			name:  "both bases empty omits the section",
			usage: store.ShiftUsage{WorkItemID: 7, Team: "bronze"},
			alias: "ploeg-abcdef012345",
		},
		{
			name:  "grafana only",
			links: reportLinkConfig{GrafanaURL: grafana + "/"},
			usage: store.ShiftUsage{WorkItemID: 7, Team: "bronze"},
			alias: "ploeg-abcdef012345",
			want: []string{
				"- [Unfold — Loop dashboard](https://grafana.example/d/glide-loop?var-team=bronze)\n",
				"- [Run Explorer](https://grafana.example/d/dark-factory-run-explorer?var-run=ploeg-abcdef012345)\n",
				"- [Spend & Attribution](https://grafana.example/d/dark-factory-spend-attribution)\n",
			},
			notWant: []string{"vloer", "#work", "spend-attribution?"},
		},
		{
			name:    "vloer only",
			links:   reportLinkConfig{VloerURL: vloer + "/"},
			usage:   store.ShiftUsage{WorkItemID: 7, Team: "bronze"},
			alias:   "ploeg-abcdef012345",
			want:    []string{"- [This Work Item in Vloer](https://vloer.example/#work/7)\n"},
			notWant: []string{"grafana", "Run Explorer", "/ploeg"},
		},
		{
			name:    "no alias and no team",
			links:   reportLinkConfig{GrafanaURL: grafana, VloerURL: vloer},
			usage:   store.ShiftUsage{WorkItemID: 7, Team: "  "},
			want:    []string{"(https://grafana.example/d/glide-loop)\n", "(https://grafana.example/d/dark-factory-spend-attribution)\n", "(https://vloer.example/#work/7)\n"},
			notWant: []string{"Run Explorer", "var-team"},
		},
		{
			name:    "no Work Item id links Vloer's Work page",
			links:   reportLinkConfig{VloerURL: vloer},
			want:    []string{"- [Work in Vloer](https://vloer.example/#work)\n"},
			notWant: []string{"#work/"},
		},
		{
			name:  "query values are escaped",
			links: reportLinkConfig{GrafanaURL: grafana},
			usage: store.ShiftUsage{Team: "red & blue (ops)"},
			alias: "ploeg run#1",
			want: []string{
				"(https://grafana.example/d/glide-loop?var-team=red+%26+blue+%28ops%29)",
				"(https://grafana.example/d/dark-factory-run-explorer?var-run=ploeg+run%231)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := linksSection(tc.links, tc.usage, tc.alias)
			if len(tc.want) == 0 {
				if got != "" {
					t.Fatalf("linksSection = %q, want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, "**Where to dig deeper**\n\n") {
				t.Errorf("section heading missing:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q:\n%s", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("unexpected %q:\n%s", nw, got)
				}
			}
		})
	}
}

func TestMoneyFormatsNlNL(t *testing.T) {
	for in, want := range map[float64]string{
		0:      "US$ 0,00",
		0.06:   "US$ 0,06",
		0.005:  "US$ 0,01",
		12.345: "US$ 12,35",
		1234.5: "US$ 1.234,50",
	} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
	if got := tokenCount(15004); got != "15.004" {
		t.Errorf("tokenCount = %q, want 15.004", got)
	}
}

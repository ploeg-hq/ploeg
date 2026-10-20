package taskprompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// OrientationMinutes and OrientationToolCalls bound how long a writing Run
// is told to orient before it writes its plan (ADR-0133). They are guidance:
// exceeding them stops nothing.
const (
	OrientationMinutes   = 10
	OrientationToolCalls = 30
)

func writeYourRun(b *strings.Builder, spec harness.TaskSpec, writes bool) {
	l := spec.Limits
	var lines []string
	if t := timeLine(l); t != "" {
		lines = append(lines, t)
	}
	if l.BudgetUSD > 0 {
		lines = append(lines, fmt.Sprintf("Model budget: US$ %.2f for this Run. Model calls past it are refused.", l.BudgetUSD))
	}
	switch {
	case l.Round > 0 && l.MaxRounds >= l.Round:
		lines = append(lines, fmt.Sprintf("Round %d of at most %d in this Shift.", l.Round, l.MaxRounds))
	case l.Round > 0:
		lines = append(lines, fmt.Sprintf("Round %d of this Shift.", l.Round))
	}
	if writes {
		lines = append(lines,
			fmt.Sprintf("Orient for at most about %d minutes or %d tool calls: read what the Work Item needs, not the whole repository. Then write a short numbered plan (in your plan or todo tool if you have one), then edit.", OrientationMinutes, OrientationToolCalls),
			fmt.Sprintf("Commit after each completed plan step and push it to %[1]s, so a retry continues from your work instead of starting over. If %[1]s already exists on the forge, an earlier attempt pushed it: continue from it.", spec.Branch))
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("## Your Run\n\n")
	for _, line := range lines {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func timeLine(l harness.RunLimits) string {
	switch {
	case l.Time > 0 && l.Idle > 0:
		return fmt.Sprintf("Time: Ploeg stops this Run %s after your prompt starts, or after %s without activity (no output, tool call or model call). A stopped Run delivers nothing, so leave time to finish.", duration(l.Time), duration(l.Idle))
	case l.Time > 0:
		return fmt.Sprintf("Time: Ploeg stops this Run %s after your prompt starts. A stopped Run delivers nothing, so leave time to finish.", duration(l.Time))
	case l.Idle > 0:
		return fmt.Sprintf("Time: Ploeg stops this Run after %s without activity (no output, tool call or model call).", duration(l.Idle))
	}
	return ""
}

func duration(d time.Duration) string {
	if d < time.Minute {
		return plural(int(d/time.Second), "second")
	}
	return plural(int(d/time.Minute), "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

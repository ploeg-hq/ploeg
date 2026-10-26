package worker

import (
	"io"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

func (w *Worker) harnessOutput(podStream io.Writer, tail *harness.TailBuffer) io.Writer {
	if w.Cfg.HarnessOutputInLog {
		return io.MultiWriter(podStream, tail)
	}
	return tail
}

func redactedOutput(output []byte, leaks *leakWatch) []byte {
	return []byte(transcript.NewRedactor(leaks.values()...).String(string(output)))
}

func reportLogAttrs(report harness.OutcomeReport) []any {
	return []any{
		"outcome", report.Outcome,
		"failure_reason", report.FailureReason,
		"summary_bytes", len(report.Summary),
		"stuck_reason_bytes", len(report.StuckReason),
		"links", linksForLog(report.Links),
	}
}

func linksForLog(links []string) []string {
	if len(links) == 0 {
		return nil
	}
	out := make([]string, len(links))
	for i, link := range links {
		u, err := url.Parse(link)
		if err != nil || u.Host == "" {
			out[i] = "[unparsed link]"
			continue
		}
		out[i] = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
	}
	return out
}

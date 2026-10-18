package worker

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const testRunAlias = "ploeg-runalias"

func TestParseTelemetryAcceptsTheChartShapeAndFillsDefaults(t *testing.T) {
	got, err := ParseTelemetry(`{"endpoint":"http://alloy-gateway.observability.svc.cluster.local:4318","resourceAttributes":{"deployment.environment":"homelab"},"logToolDetails":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "http/protobuf" {
		t.Fatalf("protocol = %q, want the http/protobuf default", got.Protocol)
	}
	if !slices.Equal(got.Signals, []string{"metrics", "logs", "traces"}) {
		t.Fatalf("signals = %v, want all three by default", got.Signals)
	}
	if got.LogPrompts {
		t.Fatal("prompt text must stay off unless asked for")
	}
}

func TestParseTelemetryEmptyMeansNothingIsExported(t *testing.T) {
	got, err := ParseTelemetry("  ")
	if err != nil || got != nil {
		t.Fatalf("ParseTelemetry(blank) = %v, %v; want nil, nil", got, err)
	}
	if env := telemetryEnvironment(nil, "claude-code", "", "bronze", "builder", testRunAlias); env != nil {
		t.Fatalf("no setting exported %v", env)
	}
}

func TestParseTelemetryRejectsWhatWouldMisleadOrLeak(t *testing.T) {
	for name, raw := range map[string]string{
		"not a URL":              `{"endpoint":"alloy:4318"}`,
		"unknown scheme":         `{"endpoint":"ftp://alloy:4318"}`,
		"unknown protocol":       `{"endpoint":"http://alloy:4318","protocol":"thrift"}`,
		"unknown signal":         `{"endpoint":"http://alloy:4318","signals":["profiles"]}`,
		"repeated signal":        `{"endpoint":"http://alloy:4318","signals":["logs","logs"]}`,
		"worker-owned attribute": `{"endpoint":"http://alloy:4318","resourceAttributes":{"ploeg.run.alias":"spoofed"}}`,
		"attribute with a comma": `{"endpoint":"http://alloy:4318","resourceAttributes":{"team":"a,b"}}`,
		"misspelt field":         `{"endpoint":"http://alloy:4318","logPrompt":true}`,
	} {
		if _, err := ParseTelemetry(raw); err == nil {
			t.Errorf("%s: %s was accepted", name, raw)
		}
	}
}

func TestTelemetryEnvironmentPerHarness(t *testing.T) {
	setting := &Telemetry{Endpoint: "http://alloy:4318", Protocol: "http/protobuf", Signals: []string{"metrics", "logs", "traces"}, ResourceAttributes: map[string]string{"deployment.environment": "homelab"}, LogToolDetails: true}
	for _, tc := range []struct {
		harness, profile string
		want, absent     []string
	}{
		{
			harness: "claude-code",
			want:    []string{"CLAUDE_CODE_ENABLE_TELEMETRY=1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1", "OTEL_LOG_TOOL_DETAILS=1", "OTEL_LOG_USER_PROMPTS=0", "OTEL_TRACES_EXPORTER=otlp"},
		},
	} {
		env := telemetryEnvironment(setting, tc.harness, tc.profile, "bronze", "builder", testRunAlias)
		for _, kv := range tc.want {
			if !slices.Contains(env, kv) {
				t.Errorf("%s/%s: missing %s in %v", tc.harness, tc.profile, kv, env)
			}
		}
		for _, key := range tc.absent {
			for _, kv := range env {
				if strings.HasPrefix(kv, key+"=") {
					t.Errorf("%s/%s: set %s, which belongs to another runtime", tc.harness, tc.profile, kv)
				}
			}
		}
		if !slices.Contains(env, "OTEL_RESOURCE_ATTRIBUTES=deployment.environment=homelab,ploeg.role=builder,ploeg.run.alias="+testRunAlias+",ploeg.team=bronze") {
			t.Errorf("%s: resource attributes do not carry the Run's team, role and alias: %v", tc.harness, env)
		}
	}
}

func TestTelemetryEnvironmentTurnsOffSignalsThatWereNotAskedFor(t *testing.T) {
	env := telemetryEnvironment(&Telemetry{Endpoint: "http://alloy:4318", Protocol: "grpc", Signals: []string{"logs"}}, "claude-code", "", "pewter", "", testRunAlias)
	for _, kv := range []string{"OTEL_LOGS_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=none", "OTEL_TRACES_EXPORTER=none", "OTEL_RESOURCE_ATTRIBUTES=ploeg.run.alias=" + testRunAlias + ",ploeg.team=pewter"} {
		if !slices.Contains(env, kv) {
			t.Errorf("missing %s in %v", kv, env)
		}
	}
	if slices.Contains(env, "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1") {
		t.Error("enabled trace instrumentation for a setting without traces")
	}
}

func TestTelemetryEnvironmentNeverCarriesTheRunToken(t *testing.T) {
	token := strings.Repeat("7", 32)
	env := telemetryEnvironment(&Telemetry{Endpoint: "http://alloy:4318", Protocol: "http/protobuf", Signals: telemetrySignals}, "claude-code", "", "bronze", "builder", "ploeg-"+token[:12])
	for _, kv := range env {
		if strings.Contains(kv, token) {
			t.Fatalf("%s exposes the full Run token", kv)
		}
	}
}

func TestTelemetrySupportNamesWhatEachHarnessExports(t *testing.T) {
	optIn := []string{"openhands"}
	for _, tc := range []struct {
		harness, profile string
		allow            []string
		want             []string
	}{
		{"claude-code", "", nil, []string{"metrics", "logs", "traces"}},
		{"openhands", "", nil, nil},
		{"acp", "openhands", nil, nil},
		{"openhands", "", optIn, []string{"traces"}},
		{"acp", "openhands", optIn, []string{"traces"}},
		{"acp", "opencode", optIn, nil},
		{"exec", "", optIn, nil},
		{"claude-environment", "", optIn, nil},
	} {
		if got := TelemetrySupport(tc.harness, tc.profile, tc.allow); !slices.Equal(got, tc.want) {
			t.Errorf("TelemetrySupport(%s, %s, %v) = %v, want %v", tc.harness, tc.profile, tc.allow, got, tc.want)
		}
	}
}

func TestParseTelemetryAcceptsOnlyOpenHandsAsAnOptIn(t *testing.T) {
	got, err := ParseTelemetry(`{"endpoint":"http://alloy:4318","allowHarnesses":["openhands"]}`)
	if err != nil || !slices.Equal(got.AllowHarnesses, []string{"openhands"}) {
		t.Fatalf("ParseTelemetry(openhands opt-in) = %v, %v", got, err)
	}
	for name, raw := range map[string]string{
		"unknown harness": `{"endpoint":"http://alloy:4318","allowHarnesses":["opencode"]}`,
		"repeated entry":  `{"endpoint":"http://alloy:4318","allowHarnesses":["openhands","openhands"]}`,
	} {
		if _, err := ParseTelemetry(raw); err == nil {
			t.Errorf("%s: %s was accepted", name, raw)
		}
	}
}

func TestOpenHandsOptInGetsTheStandardVariablesOnly(t *testing.T) {
	setting := &Telemetry{Endpoint: "http://alloy:4318", Protocol: "http/protobuf", Signals: telemetrySignals, AllowHarnesses: []string{"openhands"}}
	for _, profile := range []struct{ harness, profile string }{{"openhands", ""}, {"acp", "openhands"}} {
		env := telemetryEnvironment(setting, profile.harness, profile.profile, "bronze", "builder", testRunAlias)
		if envValue(env, "OTEL_EXPORTER_OTLP_ENDPOINT") != "http://alloy:4318" {
			t.Errorf("%s/%s with the opt-in got no endpoint: %v", profile.harness, profile.profile, env)
		}
		if envValue(env, "CLAUDE_CODE_ENABLE_TELEMETRY") != "" {
			t.Errorf("%s/%s got Claude Code's variables: %v", profile.harness, profile.profile, env)
		}
	}
}

type namedRecorder struct {
	contextAdapter
	name string
}

func (a *namedRecorder) Name() string { return a.name }

func runRecordingTelemetry(t *testing.T, harnessName string, allow ...string) *namedRecorder {
	t.Helper()
	forgeURL := gitForge(t, map[string]string{"main.go": "package main\n"})
	adapter := &namedRecorder{name: harnessName}
	w := New(Config{APIURL: contextAPI(t, nil), ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir(), RepoKnowledgeDir: "-", Team: "bronze",
		Telemetry: &Telemetry{Endpoint: "http://alloy:4318", Protocol: "http/protobuf", Signals: []string{"traces"}, AllowHarnesses: allow}},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "reviewer", PreAuthor: true, WorkItem: work.WorkItem{ID: "7", Title: "read"}}
	w.execute(context.Background(), claimed, "agent/vik-7", testRunAlias, "", "")
	if !adapter.ran {
		t.Fatal("the harness never ran")
	}
	return adapter
}

func TestWorkerHandsTheTelemetrySettingToTheHarness(t *testing.T) {
	adapter := runRecordingTelemetry(t, "claude-code")
	for key, want := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":  "http://alloy:4318",
		"OTEL_TRACES_EXPORTER":         "otlp",
		"OTEL_LOGS_EXPORTER":           "none",
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_RESOURCE_ATTRIBUTES":     "ploeg.role=reviewer,ploeg.run.alias=" + testRunAlias + ",ploeg.team=bronze",
	} {
		if got := envValue(adapter.env, key); got != want {
			t.Errorf("harness %s = %q, want %q", key, got, want)
		}
	}
}

func TestWorkerWithholdsTelemetryFromOpenHands(t *testing.T) {
	adapter := runRecordingTelemetry(t, "openhands")
	for _, kv := range adapter.env {
		if strings.HasPrefix(kv, "OTEL_") {
			t.Fatalf("OpenHands got %s; with an OTLP endpoint set every prompt fails", kv)
		}
	}
}

func TestWorkerWithoutTelemetryHandsTheHarnessNoCollector(t *testing.T) {
	_, adapter := runWithContext(t, nil, nil, work.WorkItem{ID: "7", Title: "read"})
	for _, kv := range adapter.env {
		if strings.HasPrefix(kv, "OTEL_") || strings.HasPrefix(kv, "CLAUDE_CODE_ENABLE_TELEMETRY=") {
			t.Fatalf("harness got %s with no telemetry configured", kv)
		}
	}
}

func TestHarnessesWithoutSafeTelemetryGetNoOTELVariables(t *testing.T) {
	setting := &Telemetry{Endpoint: "http://alloy:4318", Protocol: "http/protobuf", Signals: telemetrySignals}
	for _, tc := range []struct{ harness, profile string }{
		{"openhands", ""},
		{"acp", "openhands"},
		{"acp", "opencode"},
		{"exec", ""},
		{"claude-environment", ""},
	} {
		if env := telemetryEnvironment(setting, tc.harness, tc.profile, "bronze", "builder", testRunAlias); env != nil {
			t.Errorf("%s/%s got %v; an OTLP endpoint made every OpenHands prompt fail", tc.harness, tc.profile, env)
		}
		if TelemetryWithheldReason(tc.harness, tc.profile) == "" {
			t.Errorf("%s/%s withholds telemetry without saying why", tc.harness, tc.profile)
		}
	}
}

func TestWorkerHandsOptedInOpenHandsItsTraces(t *testing.T) {
	adapter := runRecordingTelemetry(t, "openhands", "openhands")
	if got := envValue(adapter.env, "OTEL_TRACES_EXPORTER"); got != "otlp" {
		t.Fatalf("opted-in OpenHands OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	if got := envValue(adapter.env, "OTEL_RESOURCE_ATTRIBUTES"); got != "ploeg.role=reviewer,ploeg.run.alias="+testRunAlias+",ploeg.team=bronze" {
		t.Fatalf("opted-in OpenHands resource attributes = %q", got)
	}
}

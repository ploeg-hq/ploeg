package worker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Telemetry is where a Run's harness exports OpenTelemetry signals
// (PLOEG_TELEMETRY). Nil exports nothing, which is the behaviour before the
// setting existed.
type Telemetry struct {
	Endpoint string `json:"endpoint"`
	// Protocol is the OTLP transport: http/protobuf (default), http/json or grpc.
	Protocol string `json:"protocol,omitempty"`
	// Signals lists what to export: any of metrics, logs and traces. Empty
	// means all three.
	Signals []string `json:"signals,omitempty"`
	// ResourceAttributes are added to every signal. The worker adds
	// ploeg.team, ploeg.role and ploeg.run.alias itself.
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
	// LogPrompts exports prompt text where the harness supports it. Off by
	// default: prompts can carry ticket content and personal data.
	LogPrompts bool `json:"logPrompts,omitempty"`
	// LogToolDetails exports tool names and arguments where the harness
	// supports it.
	LogToolDetails bool `json:"logToolDetails,omitempty"`
}

var telemetrySignals = []string{"metrics", "logs", "traces"}

var telemetryProtocols = map[string]bool{"http/protobuf": true, "http/json": true, "grpc": true}

var reservedResourceAttributes = map[string]bool{"ploeg.team": true, "ploeg.role": true, "ploeg.run.alias": true}

// ParseTelemetry decodes PLOEG_TELEMETRY. Empty input means no telemetry.
func ParseTelemetry(raw string) (*Telemetry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var t Telemetry
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("PLOEG_TELEMETRY: %w", err)
	}
	u, err := url.Parse(t.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("PLOEG_TELEMETRY: endpoint %q must be an http or https URL", t.Endpoint)
	}
	if t.Protocol == "" {
		t.Protocol = "http/protobuf"
	}
	if !telemetryProtocols[t.Protocol] {
		return nil, fmt.Errorf("PLOEG_TELEMETRY: protocol %q is not http/protobuf, http/json or grpc", t.Protocol)
	}
	seen := map[string]bool{}
	for _, s := range t.Signals {
		if !slices.Contains(telemetrySignals, s) {
			return nil, fmt.Errorf("PLOEG_TELEMETRY: signal %q is not metrics, logs or traces", s)
		}
		if seen[s] {
			return nil, fmt.Errorf("PLOEG_TELEMETRY: signal %q is listed twice", s)
		}
		seen[s] = true
	}
	if len(t.Signals) == 0 {
		t.Signals = append([]string{}, telemetrySignals...)
	}
	for k, v := range t.ResourceAttributes {
		if reservedResourceAttributes[k] {
			return nil, fmt.Errorf("PLOEG_TELEMETRY: resource attribute %q is set by the worker", k)
		}
		if k == "" || strings.ContainsAny(k, "=, ") || strings.ContainsAny(v, "=,") {
			return nil, fmt.Errorf("PLOEG_TELEMETRY: resource attribute %q=%q is malformed", k, v)
		}
	}
	return &t, nil
}

func (t *Telemetry) exports(signal string) bool {
	return t != nil && slices.Contains(t.Signals, signal)
}

// TelemetrySupport names the signals a harness exports natively, so the
// worker can say at startup what a telemetry setting will and will not reach.
func TelemetrySupport(harnessName, acpProfile string) []string {
	switch {
	case harnessName == "claude-code":
		return telemetrySignals
	case harnessName == "openhands", harnessName == "acp" && acpProfile == "openhands":
		return []string{"traces"}
	default:
		return nil
	}
}

// telemetryEnvironment is the harness environment that points a Run's agent
// at the configured collector. The standard OTEL_* variables are set for
// every harness, since an agent that does not read them ignores them; the
// variables a runtime needs on top are added for that runtime only.
func telemetryEnvironment(t *Telemetry, harnessName, acpProfile, team, role, alias string) []string {
	if t == nil {
		return nil
	}
	env := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + t.Endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL=" + t.Protocol,
		"OTEL_RESOURCE_ATTRIBUTES=" + resourceAttributes(t.ResourceAttributes, team, role, alias),
	}
	for _, signal := range telemetrySignals {
		exporter := "none"
		if t.exports(signal) {
			exporter = "otlp"
		}
		env = append(env, "OTEL_"+strings.ToUpper(signal)+"_EXPORTER="+exporter)
	}
	if harnessName == "claude-code" {
		env = append(env, "CLAUDE_CODE_ENABLE_TELEMETRY=1", "OTEL_LOG_USER_PROMPTS="+onOff(t.LogPrompts), "OTEL_LOG_TOOL_DETAILS="+onOff(t.LogToolDetails))
		if t.exports("traces") {
			env = append(env, "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1")
		}
	}
	return env
}

func resourceAttributes(extra map[string]string, team, role, alias string) string {
	attrs := map[string]string{}
	for k, v := range extra {
		attrs[k] = v
	}
	for k, v := range map[string]string{"ploeg.team": team, "ploeg.role": role, "ploeg.run.alias": alias} {
		if v != "" {
			attrs[k] = v
		}
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+attrs[k])
	}
	return strings.Join(pairs, ",")
}

func (w *Worker) runTelemetryEnvironment(claimedRole, alias string) []string {
	role := claimedRole
	if role == "" {
		role = w.Cfg.Role
	}
	name, profile := w.harnessIdentity()
	return telemetryEnvironment(w.Cfg.Telemetry, name, profile, w.Cfg.Team, role, alias)
}

func (w *Worker) harnessIdentity() (name, profile string) {
	if w.Adapter == nil {
		return "", ""
	}
	if p, ok := w.Adapter.(profiledAdapter); ok {
		profile = p.Profile()
	}
	return w.Adapter.Name(), profile
}

func onOff(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

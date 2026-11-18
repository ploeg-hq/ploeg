package toolregistry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProbeProtocolVersion is the MCP version the probe asks for.
const ProbeProtocolVersion = "2025-06-18"

const maxProbeResponse = 1 << 20

// OfferedTool is a tool a server lists, with the hints it gives.
type OfferedTool struct {
	Name            string `json:"name"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
}

// ProbeResult is what a server said to the probe.
type ProbeResult struct {
	ProtocolVersion string        `json:"protocolVersion"`
	Tools           []OfferedTool `json:"tools"`
}

// Prober connects to a server as the gateway would and lists its tools.
type Prober struct {
	Client *http.Client
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Probe runs initialize, notifications/initialized and tools/list against
// a server with credential attached as auth says. It never sends the
// credential anywhere but the server's URL and never follows a redirect.
func (p *Prober) Probe(ctx context.Context, serverURL string, auth Auth, credential string) (ProbeResult, *Error) {
	c := probeConn{client: p.Client, url: serverURL, auth: auth, credential: credential}
	init, e := c.call(ctx, 1, "initialize", map[string]any{
		"protocolVersion": ProbeProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ploeg-probe", "version": "1"},
	})
	if e != nil {
		return ProbeResult{}, e
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(init, &initResult) != nil || initResult.ProtocolVersion == "" {
		return ProbeResult{}, probeFault("The server's initialize result has no protocolVersion.")
	}
	c.protocol = initResult.ProtocolVersion
	if e := c.notify(ctx, "notifications/initialized"); e != nil {
		return ProbeResult{}, e
	}
	out := ProbeResult{ProtocolVersion: initResult.ProtocolVersion, Tools: []OfferedTool{}}
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, e := c.call(ctx, int64(page+2), "tools/list", params)
		if e != nil {
			return ProbeResult{}, e
		}
		var list struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint    *bool `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return ProbeResult{}, probeFault("The server's tools/list result is not a tool list.")
		}
		for _, t := range list.Tools {
			if len(out.Tools) >= 1000 {
				break
			}
			out.Tools = append(out.Tools, OfferedTool{Name: t.Name, ReadOnlyHint: t.Annotations.ReadOnlyHint, DestructiveHint: t.Annotations.DestructiveHint})
		}
		if list.NextCursor == "" {
			break
		}
		cursor = list.NextCursor
	}
	return out, nil
}

func probeFault(message string) *Error { return refuse(422, "probe_failed", message) }

type probeConn struct {
	client     *http.Client
	url        string
	auth       Auth
	credential string
	session    string
	protocol   string
}

func (c *probeConn) request(ctx context.Context, body []byte) (*http.Response, *Error) {
	if !outboundURL.MatchString(c.url) {
		return nil, probeFault("The server URL is not usable.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, probeFault("The server URL is not usable.")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.protocol != "" {
		req.Header.Set("Mcp-Protocol-Version", c.protocol)
	}
	switch c.auth.Kind {
	case AuthBearer, AuthOAuthClientCredentials:
		req.Header.Set("Authorization", "Bearer "+c.credential)
	case AuthHeader:
		req.Header.Set(c.auth.Header, c.credential)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, probeFault("The server could not be reached: " + redact(err.Error(), c.credential))
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, probeFault("The server answered with a redirect; Ploeg never follows one. Register the final URL.")
	}
	return resp, nil
}

func redact(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = nil
		s = u.String()
	}
	return s
}

func (c *probeConn) notify(ctx context.Context, method string) *Error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	resp, e := c.request(ctx, body)
	if e != nil {
		return e
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeResponse))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return probeFault(fmt.Sprintf("The server refused %s with HTTP %d.", method, resp.StatusCode))
	}
	return nil
}

func (c *probeConn) call(ctx context.Context, id int64, method string, params any) (json.RawMessage, *Error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	resp, e := c.request(ctx, body)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, refuse(422, "credential_refused", fmt.Sprintf("The server refused the credential (HTTP %d).", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, probeFault(fmt.Sprintf("The server answered %s with HTTP %d.", method, resp.StatusCode))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" && c.session == "" {
		c.session = s
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponse+1))
	if err != nil || len(data) > maxProbeResponse {
		return nil, probeFault("The server's answer is larger than 1 MiB.")
	}
	var messages [][]byte
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		messages = sseData(data)
	} else {
		messages = [][]byte{data}
	}
	want := fmt.Sprint(id)
	for _, m := range messages {
		var r rpcResponse
		if json.Unmarshal(m, &r) != nil || r.JSONRPC != "2.0" {
			continue
		}
		if r.Method != "" && len(r.ID) > 0 {
			return nil, probeFault(fmt.Sprintf("The server sent the request %q to the client; Ploeg answers none.", r.Method))
		}
		if strings.Trim(string(r.ID), `"`) != want {
			continue
		}
		if r.Error != nil {
			return nil, probeFault(fmt.Sprintf("The server answered %s with error %d.", method, r.Error.Code))
		}
		return r.Result, nil
	}
	return nil, probeFault(fmt.Sprintf("The server sent no answer to %s.", method))
}

func sseData(data []byte) [][]byte {
	var out [][]byte
	var cur []byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), maxProbeResponse)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if len(cur) > 0 {
				cur = append(cur, '\n')
			}
			cur = append(cur, strings.TrimPrefix(v, " ")...)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// CompareProbe checks the allowed tool list against what the server
// offers: a listed tool the server does not offer, and a read tool the
// server marks destructive or not read-only, each fail a check. A
// conflict is a refusal; a missing tool is reported.
func CompareProbe(tools []Tool, offered ProbeResult) ([]Check, *Error) {
	byName := map[string]OfferedTool{}
	for _, o := range offered.Tools {
		byName[o.Name] = o
	}
	checks := []Check{{Name: "protocol", OK: true, Detail: offered.ProtocolVersion}}
	var conflict *Error
	for _, t := range tools {
		o, ok := byName[t.Name]
		if !ok {
			checks = append(checks, Check{Name: "tool_not_offered", OK: false, Detail: t.Name})
			continue
		}
		destructive := o.DestructiveHint != nil && *o.DestructiveHint
		notReadOnly := o.ReadOnlyHint != nil && !*o.ReadOnlyHint
		if t.Access == AccessRead && (destructive || notReadOnly) {
			checks = append(checks, Check{Name: "tool_access_conflicts", OK: false, Detail: t.Name})
			if conflict == nil {
				conflict = refuseField(422, "tool_access_conflicts", "tools", fmt.Sprintf("The server marks %q as writing or destructive; it cannot be registered read.", t.Name))
			}
			continue
		}
		checks = append(checks, Check{Name: "tool_offered", OK: true, Detail: t.Name})
	}
	if conflict != nil {
		conflict.Checks = checks
	}
	return checks, conflict
}

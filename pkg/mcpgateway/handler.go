package mcpgateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/untrusted"
)

// Prefix is the path the credential gateway mounts Handler under.
const Prefix = "/mcp/"

const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeRefused        = -32001
	codeUpstream       = -32002
)

var allowedMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"notifications/cancelled":   true,
	"ping":                      true,
	"tools/list":                true,
	"tools/call":                true,
}

var forwardedHeaders = []string{"Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID"}

// Handler serves the MCP route family: POST /mcp/{server key}. The
// transport's GET stream and DELETE of a session are answered 405, so a
// server can never push a message the gateway did not ask for.
type Handler struct {
	Grants GrantSource
	Audit  AuditSink
	Client *http.Client
	Limits Limits
	Now    func() time.Time

	once     sync.Once
	client   *http.Client
	limits   Limits
	mu       sync.Mutex
	sessions map[string]sessionBinding
	buckets  map[int64]*bucket
}

type sessionBinding struct {
	run    int64
	server string
}

type bucket struct {
	tokens float64
	last   time.Time
	total  int
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type refusal struct {
	status  int
	code    int
	reason  string
	message string
}

func (h *Handler) init() {
	h.once.Do(func() {
		base := h.Client
		if base == nil {
			base = &http.Client{Timeout: 120 * time.Second}
		}
		c := *base
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		h.client = &c
		h.limits = h.Limits.orDefault()
		h.sessions = map[string]sessionBinding{}
		h.buckets = map[int64]*bucket{}
	})
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	start := h.now()
	call := Call{Outcome: "allowed"}
	defer func() {
		call.Duration = h.now().Sub(start)
		if h.Audit != nil {
			h.Audit.Record(r.Context(), call)
		}
	}()

	key, ok := serverKey(r.URL.Path)
	call.Server = key
	if !ok {
		h.refuseHTTP(w, &call, http.StatusNotFound, "unknown_route", "No MCP server is served at this path.")
		return
	}
	capability, ok := bearer(r)
	if !ok {
		h.refuseHTTP(w, &call, http.StatusUnauthorized, "capability_required", "Send the Run's capability as the bearer.")
		return
	}
	grant, err := h.Grants.Grant(r.Context(), capability, r)
	switch {
	case errors.Is(err, ErrRunSettled):
		h.refuseHTTP(w, &call, http.StatusGone, "run_settled", "The Run has settled; its capability reaches nothing.")
		return
	case errors.Is(err, ErrUnknownCapability):
		h.refuseHTTP(w, &call, http.StatusUnauthorized, "capability_unknown", "The capability is not a live Run's.")
		return
	case err != nil:
		h.refuseHTTP(w, &call, http.StatusServiceUnavailable, "grant_unavailable", "The Run's grant could not be read.")
		return
	}
	call.Run, call.WorkItem, call.Tenant = grant.RunID, grant.WorkItem, grant.Tenant
	server, ok := grant.Servers[key]
	if !ok {
		h.refuseHTTP(w, &call, http.StatusForbidden, "server_not_granted", "The Run's Role was not given this MCP server.")
		return
	}
	call.ServerID = server.ID
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.refuseHTTP(w, &call, http.StatusMethodNotAllowed, "method_not_allowed", "The gateway accepts JSON-RPC over POST only.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.limits.MaxRequestBytes+1))
	call.BytesIn = len(body)
	if err != nil {
		h.refuseHTTP(w, &call, http.StatusBadRequest, "unreadable_request", "The request body could not be read.")
		return
	}
	if int64(len(body)) > h.limits.MaxRequestBytes {
		h.refuseHTTP(w, &call, http.StatusRequestEntityTooLarge, "request_too_large", "A request is at most 1 MiB.")
		return
	}
	msg, ref := parseRequest(body)
	if ref != nil {
		h.refuseRPC(w, &call, nil, ref)
		return
	}
	call.Method = msg.Method
	if !allowedMethods[msg.Method] {
		h.refuseRPC(w, &call, msg.ID, &refusal{code: codeMethodNotFound, reason: "method_not_allowed", message: "The gateway does not forward this method."})
		return
	}
	var tool string
	if msg.Method == "tools/call" {
		name, ok := toolName(msg.Params)
		if !ok {
			h.refuseRPC(w, &call, msg.ID, &refusal{code: codeInvalidParams, reason: "invalid_params", message: "tools/call needs a tool name."})
			return
		}
		tool = name
		call.Tool = tool
		access, granted := server.Tools[tool]
		call.Access = access
		if !granted {
			h.refuseRPC(w, &call, msg.ID, &refusal{code: codeRefused, reason: "tool_not_granted", message: "The Run's Role was not given this tool."})
			return
		}
		if access != AccessRead && (server.ReadOnly || access != AccessWrite) {
			h.refuseRPC(w, &call, msg.ID, &refusal{code: codeRefused, reason: "write_tool_refused", message: "The Run's Role may only read, and this tool writes."})
			return
		}
		if !h.take(grant.RunID) {
			h.refuseRPC(w, &call, msg.ID, &refusal{code: codeRefused, reason: "rate_limited", message: "The Run has used its tool calls for now."})
			return
		}
	}
	session := r.Header.Get("Mcp-Session-Id")
	if session != "" && !h.sessionBelongs(session, grant.RunID, key) {
		h.refuseHTTP(w, &call, http.StatusNotFound, "session_not_bound", "This MCP session is not the Run's.")
		return
	}

	forward, err := json.Marshal(msg)
	if err != nil {
		h.refuseRPC(w, &call, msg.ID, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "invalid_request", message: "The request is not a JSON-RPC 2.0 message."})
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, server.URL, bytes.NewReader(forward))
	if err != nil {
		h.refuseHTTP(w, &call, http.StatusBadGateway, "upstream_unreachable", "The MCP server's address is unusable.")
		return
	}
	for _, name := range forwardedHeaders {
		if v := r.Header.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	attach(upstream, server)
	resp, err := h.client.Do(upstream)
	if err != nil {
		h.refuseHTTP(w, &call, http.StatusBadGateway, "upstream_unreachable", "The MCP server could not be reached.")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	h.answer(w, &call, resp, msg, server, key, tool, capability, grant.RunID)
}

func (h *Handler) answer(w http.ResponseWriter, call *Call, resp *http.Response, req rpcMessage, server ServerGrant, key, tool, capability string, run int64) {
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_redirected", "The MCP server answered with a redirect, which the gateway never follows.")
		return
	case resp.StatusCode == http.StatusNotFound && req.Method != "initialize":
		h.refuseHTTP(w, call, http.StatusNotFound, "upstream_session_expired", "The MCP server no longer knows this session.")
		return
	case resp.StatusCode >= 400:
		h.refuseRPC(w, call, req.ID, &refusal{status: http.StatusBadGateway, code: codeUpstream, reason: "upstream_error", message: fmt.Sprintf("The MCP server answered HTTP %d.", resp.StatusCode)})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, h.limits.MaxResponseBytes+1))
	call.BytesOut = len(raw)
	if err != nil {
		h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_unreadable", "The MCP server's answer could not be read.")
		return
	}
	if int64(len(raw)) > h.limits.MaxResponseBytes {
		h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_too_large", "The MCP server's answer exceeded 1 MiB.")
		return
	}
	secrets := leakForms(server.Credential, capability)
	if leaks(secrets, raw) {
		h.refuseLeak(w, call)
		return
	}
	if len(req.ID) == 0 {
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_error", "The MCP server did not accept the notification.")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		call.Status = http.StatusAccepted
		return
	}
	messages, ref := h.decodeResponse(resp.Header.Get("Content-Type"), raw)
	if ref != nil {
		h.refuseRPC(w, call, req.ID, ref)
		return
	}
	var reply *rpcMessage
	for i := range messages {
		m := messages[i]
		if decoded, err := json.Marshal(m); err != nil || leaks(secrets, decoded) {
			h.refuseLeak(w, call)
			return
		}
		if m.Method != "" && len(m.ID) > 0 {
			h.refuseRPC(w, call, req.ID, &refusal{status: http.StatusBadGateway, code: codeUpstream, reason: "upstream_requested", message: "The MCP server sent a request of its own, which the gateway never forwards."})
			return
		}
		if m.Method == "" && bytes.Equal(m.ID, req.ID) {
			reply = &messages[i]
		}
	}
	if reply == nil {
		h.refuseRPC(w, call, req.ID, &refusal{status: http.StatusBadGateway, code: codeUpstream, reason: "upstream_no_reply", message: "The MCP server sent no answer to the request."})
		return
	}
	out, ref := rewrite(*reply, req.Method, server, key, tool, h.limits)
	if ref != nil {
		h.refuseRPC(w, call, req.ID, ref)
		return
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		if !h.bindSession(s, run, key) {
			h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_session_taken", "The MCP server issued a session another Run holds.")
			return
		}
		w.Header().Set("Mcp-Session-Id", s)
	}
	out.ID, out.JSONRPC = req.ID, "2.0"
	encoded, err := json.Marshal(out)
	if err != nil || leaks(secrets, encoded) {
		h.refuseLeak(w, call)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(encoded, '\n'))
	call.Status = http.StatusOK
}

func (h *Handler) refuseLeak(w http.ResponseWriter, call *Call) {
	h.refuseHTTP(w, call, http.StatusBadGateway, "upstream_leaked_credential", "The MCP server's answer carried a credential; it was withheld.")
}

func (h *Handler) refuseHTTP(w http.ResponseWriter, call *Call, status int, reason, message string) {
	call.Outcome, call.Status = reason, status
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": reason, "message": message}})
}

func (h *Handler) refuseRPC(w http.ResponseWriter, call *Call, id json.RawMessage, r *refusal) {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	call.Outcome, call.Status = r.reason, status
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	data, _ := json.Marshal(map[string]string{"reason": r.reason})
	writeRPC(w, status, rpcMessage{ID: id, Error: &rpcError{Code: r.code, Message: r.message, Data: data}})
}

func writeRPC(w http.ResponseWriter, status int, m rpcMessage) {
	m.JSONRPC = "2.0"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(m)
}

func serverKey(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, Prefix)
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

func bearer(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	if len(v) < 8 || !strings.EqualFold(v[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(v[7:])
	return token, token != ""
}

func parseRequest(body []byte) (rpcMessage, *refusal) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return rpcMessage{}, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "batch_refused", message: "The gateway accepts one JSON-RPC message per request, never a batch."}
	}
	m, err := strictMessage(trimmed, messageFields)
	if errors.Is(err, errAmbiguousJSON) {
		return rpcMessage{}, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "ambiguous_request", message: "The request repeats a key, exactly or in another case; the gateway forwards only JSON it reads one way."}
	}
	if err != nil || m.JSONRPC != "2.0" {
		return rpcMessage{}, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "invalid_request", message: "The request is not a JSON-RPC 2.0 message."}
	}
	if m.Method == "" {
		return m, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "response_refused", message: "The gateway forwards requests and notifications only."}
	}
	if m.Result != nil || m.Error != nil {
		return rpcMessage{}, &refusal{status: http.StatusBadRequest, code: codeInvalidRequest, reason: "invalid_request", message: "The request is not a JSON-RPC 2.0 message."}
	}
	return m, nil
}

func attach(r *http.Request, s ServerGrant) {
	switch s.Auth.Kind {
	case AuthBearer:
		r.Header.Set("Authorization", "Bearer "+s.Credential)
	case AuthHeader:
		r.Header.Set(s.Auth.Header, s.Credential)
	}
}

const leakWindow = 16

func leakForms(values ...string) [][]byte {
	var forms [][]byte
	for _, v := range values {
		if len(v) < 8 {
			continue
		}
		forms = append(forms, []byte(v))
		if quoted, err := json.Marshal(v); err == nil {
			forms = append(forms, quoted[1:len(quoted)-1])
		}
		for i := 0; len(v) >= leakWindow && i+leakWindow <= len(v); i++ {
			forms = append(forms, []byte(v[i:i+leakWindow]))
		}
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			forms = append(forms, []byte(enc.EncodeToString([]byte(v))))
			for shift := 0; shift < 3; shift++ {
				encoded := enc.EncodeToString(append(make([]byte, shift), v...))
				first, last := (shift+2)/3, (shift+len(v))/3
				if 4*(last-first) >= leakWindow {
					forms = append(forms, []byte(encoded[4*first:4*last]))
				}
			}
		}
	}
	return forms
}

func leaks(forms [][]byte, text []byte) bool {
	for _, f := range forms {
		if bytes.Contains(text, f) {
			return true
		}
	}
	return false
}

func (h *Handler) decodeResponse(contentType string, raw []byte) ([]rpcMessage, *refusal) {
	media, _, _ := mime.ParseMediaType(contentType)
	upstreamFault := func(reason, message string) *refusal {
		return &refusal{status: http.StatusBadGateway, code: codeUpstream, reason: reason, message: message}
	}
	switch media {
	case "application/json":
		m, err := strictMessage(raw, messageFields)
		if errors.Is(err, errAmbiguousJSON) {
			return nil, upstreamFault("upstream_ambiguous", "The MCP server's answer repeats a key, exactly or in another case.")
		}
		if err != nil {
			return nil, upstreamFault("upstream_invalid", "The MCP server's answer is not one JSON-RPC message.")
		}
		return []rpcMessage{m}, nil
	case "text/event-stream":
		var out []rpcMessage
		events := 0
		var data strings.Builder
		flush := func() *refusal {
			if data.Len() == 0 {
				return nil
			}
			events++
			if events > h.limits.MaxEvents {
				return upstreamFault("upstream_too_many_events", "The MCP server's stream exceeded the event limit.")
			}
			m, err := strictMessage([]byte(data.String()), messageFields)
			if errors.Is(err, errAmbiguousJSON) {
				return upstreamFault("upstream_ambiguous", "The MCP server's stream carried an event that repeats a key, exactly or in another case.")
			}
			if err != nil {
				return upstreamFault("upstream_invalid", "The MCP server's stream carried an event that is not JSON-RPC.")
			}
			out = append(out, m)
			data.Reset()
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 64*1024), int(h.limits.MaxResponseBytes)+1)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if ref := flush(); ref != nil {
					return nil, ref
				}
				continue
			}
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(v, " "))
			}
		}
		if ref := flush(); ref != nil {
			return nil, ref
		}
		return out, nil
	default:
		return nil, upstreamFault("upstream_invalid", "The MCP server answered with an unsupported content type.")
	}
}

func (h *Handler) take(run int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	b, ok := h.buckets[run]
	if !ok {
		b = &bucket{tokens: float64(h.limits.CallsPerMinute), last: now}
		h.buckets[run] = b
	}
	b.tokens = min(float64(h.limits.CallsPerMinute), b.tokens+now.Sub(b.last).Minutes()*float64(h.limits.CallsPerMinute))
	b.last = now
	if b.total >= h.limits.CallsPerRun || b.tokens < 1 {
		return false
	}
	b.tokens--
	b.total++
	return true
}

func (h *Handler) sessionBelongs(session string, run int64, server string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.sessions[session]
	return ok && b.run == run && b.server == server
}

func (h *Handler) bindSession(session string, run int64, server string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b, ok := h.sessions[session]; ok {
		return b.run == run && b.server == server
	}
	h.sessions[session] = sessionBinding{run: run, server: server}
	return true
}

// Forget drops the sessions and the call budget of a Run that settled.
func (h *Handler) Forget(run int64) {
	h.init()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.buckets, run)
	for s, b := range h.sessions {
		if b.run == run {
			delete(h.sessions, s)
		}
	}
}

func frameText(key, tool, text string) string {
	return untrusted.Frame(untrusted.SourceMCP(key, tool), text)
}

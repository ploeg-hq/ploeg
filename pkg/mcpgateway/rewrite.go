package mcpgateway

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/untrusted"
)

type toolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
}

type listedTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations toolAnnotations `json:"annotations"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func rewrite(reply rpcMessage, method string, server ServerGrant, key, tool string, limits Limits) (rpcMessage, *refusal) {
	fault := func(reason, message string) *refusal {
		return &refusal{status: http.StatusBadGateway, code: codeUpstream, reason: reason, message: message}
	}
	if reply.Error != nil {
		out := rpcMessage{Error: &rpcError{Code: reply.Error.Code, Message: frameText(key, tool, reply.Error.Message)}}
		return out, nil
	}
	var result json.RawMessage
	var err error
	switch method {
	case "initialize":
		result, err = rewriteInitialize(reply.Result, key)
	case "tools/list":
		result, err = rewriteToolsList(reply.Result, server, limits)
	case "tools/call":
		result, err = rewriteToolsCall(reply.Result, key, tool)
	case "ping":
		result = json.RawMessage("{}")
	}
	if err != nil {
		return rpcMessage{}, fault("upstream_invalid", err.Error())
	}
	return rpcMessage{Result: result}, nil
}

type protocolError string

func (e protocolError) Error() string { return string(e) }

func rewriteInitialize(raw json.RawMessage, key string) (json.RawMessage, error) {
	var in struct {
		ProtocolVersion string          `json:"protocolVersion"`
		ServerInfo      json.RawMessage `json:"serverInfo"`
		Instructions    string          `json:"instructions"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, protocolError("The MCP server's initialize answer is malformed.")
	}
	if !slices.Contains(ProtocolVersions, in.ProtocolVersion) {
		return nil, protocolError("The MCP server speaks a protocol version the gateway was not tested with.")
	}
	out := map[string]any{
		"protocolVersion": in.ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]string{"name": key, "version": "ploeg-gateway"},
	}
	if in.Instructions != "" {
		out["instructions"] = frameText(key, "instructions", in.Instructions)
	}
	return json.Marshal(out)
}

func rewriteToolsList(raw json.RawMessage, server ServerGrant, limits Limits) (json.RawMessage, error) {
	var in struct {
		Tools      []listedTool `json:"tools"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, protocolError("The MCP server's tool list is malformed.")
	}
	tools := []listedTool{}
	seen := map[string]bool{}
	for _, t := range in.Tools {
		access, granted := server.Tools[t.Name]
		if !granted || seen[t.Name] || (access == AccessWrite && server.ReadOnly) || (access != AccessRead && access != AccessWrite) {
			continue
		}
		seen[t.Name] = true
		if len(t.Description) > limits.MaxDescription {
			t.Description = t.Description[:limits.MaxDescription]
		}
		if len(t.InputSchema) == 0 {
			t.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		t.Annotations = toolAnnotations{ReadOnlyHint: access == AccessRead, DestructiveHint: access == AccessWrite}
		tools = append(tools, t)
	}
	out := map[string]any{"tools": tools}
	if in.NextCursor != "" {
		out["nextCursor"] = in.NextCursor
	}
	return json.Marshal(out)
}

func rewriteToolsCall(raw json.RawMessage, key, tool string) (json.RawMessage, error) {
	var in struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, protocolError("The MCP server's tool result is malformed.")
	}
	content := make([]contentItem, 0, len(in.Content))
	for _, item := range in.Content {
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(item, &c)
		if c.Type == "text" {
			content = append(content, contentItem{Type: "text", Text: frameText(key, tool, c.Text)})
			continue
		}
		content = append(content, contentItem{Type: "text", Text: withheld(c.Type)})
	}
	out := map[string]any{"content": content}
	if in.IsError {
		out["isError"] = true
	}
	return json.Marshal(out)
}

func withheld(kind string) string {
	if len(kind) > 32 {
		kind = kind[:32]
	}
	b, _ := json.Marshal(kind)
	return "Ploeg withheld content of type " + untrusted.Escape(string(b)) + " from this tool result: only text reaches the Run."
}

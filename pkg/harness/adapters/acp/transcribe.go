package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/transcript"
)

// transcribe records one session/update off the wire in the Run's transcript
// (ADR-0090). Anything it cannot read is skipped: the transcript is for
// people, and a Run never depends on it.
func transcribe(rec *transcript.Recorder, line []byte) {
	if rec == nil {
		return
	}
	var rpc rpcLine
	if json.Unmarshal(line, &rpc) != nil || rpc.Method != "session/update" {
		return
	}
	var params sessionUpdateParams
	if json.Unmarshal(rpc.Params, &params) != nil || len(params.Update) == 0 {
		return
	}
	var e updateEnvelope
	if json.Unmarshal(params.Update, &e) != nil {
		return
	}
	switch ParseUpdateKind(e.SessionUpdate) {
	case UpdateAgentMessageChunk:
		rec.Message(e.contentText())
	case UpdateAgentThoughtChunk:
		rec.Thought(e.contentText())
	case UpdateToolCall, UpdateToolCallUpdate:
		ev := transcript.ToolEvent{
			ID:     e.ToolCallID,
			Title:  e.Title,
			Status: string(ParseToolCallStatus(e.Status)),
			Input:  inputSummary(e.RawInput),
		}
		if e.Kind != "" {
			ev.Kind = string(ParseToolKind(e.Kind))
		}
		for _, l := range e.Locations {
			ev.Paths = append(ev.Paths, l.Path)
		}
		rec.Tool(ev)
		rec.ToolOutput(e.ToolCallID, toolOutput(e))
	case UpdatePlan:
		steps := make([]transcript.Step, 0, len(e.Entries))
		for _, p := range e.Entries {
			steps = append(steps, transcript.Step{Content: strings.TrimSpace(p.Content), Status: string(ParsePlanEntryStatus(p.Status))})
		}
		rec.Plan(steps)
	}
}

// inputSummary is the one line that says what a tool call was asked to do:
// its command, path, query or URL when the input names one, else the input
// as compact JSON.
func inputSummary(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		for _, key := range []string{"command", "cmd", "path", "file_path", "filePath", "file", "query", "pattern", "url"} {
			v, ok := fields[key]
			if !ok {
				continue
			}
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
			var argv []string
			if json.Unmarshal(v, &argv) == nil && len(argv) > 0 {
				return strings.Join(argv, " ")
			}
		}
	}
	return compactJSON(raw)
}

// toolOutput is what a tool call returned: its text content, its diffs, or,
// when it finished without content, its raw output.
func toolOutput(e updateEnvelope) string {
	var parts []string
	var items []toolCallContent
	if json.Unmarshal(e.Content, &items) == nil {
		for _, item := range items {
			switch item.Type {
			case "content":
				if item.Content != nil && item.Content.Text != "" {
					parts = append(parts, item.Content.Text)
				}
			case "diff":
				parts = append(parts, renderDiff(item))
			}
		}
	} else if text := e.contentText(); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		if st := ParseToolCallStatus(e.Status); st == ToolCompleted || st == ToolFailed {
			if out := rawText(e.RawOutput); out != "" {
				parts = append(parts, out)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		var texts []string
		for _, key := range []string{"output", "stdout", "stderr", "content", "result", "text", "error"} {
			if v, ok := fields[key]; ok && json.Unmarshal(v, &s) == nil && s != "" {
				texts = append(texts, s)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return compactJSON(raw)
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// renderDiff shows the lines an edit changed: the common lines at both ends
// are left out, the rest are listed as removed and added.
func renderDiff(d toolCallContent) string {
	var old string
	if d.OldText != nil {
		old = *d.OldText
	}
	a, b := splitLines(old), splitLines(d.NewText)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	removed, added := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	var out strings.Builder
	if d.OldText == nil {
		fmt.Fprintf(&out, "new file %s\n", d.Path)
	} else {
		fmt.Fprintf(&out, "edit %s\n", d.Path)
	}
	fmt.Fprintf(&out, "@@ line %d: -%d +%d @@", prefix+1, len(removed), len(added))
	for _, l := range removed {
		out.WriteString("\n-" + l)
	}
	for _, l := range added {
		out.WriteString("\n+" + l)
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

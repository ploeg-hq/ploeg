// Package transcript records what an agent did during a Run as ordered, typed
// entries and ships them to ploegd in batches (ADR-0090).
package transcript

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is the type of one transcript entry.
type Kind string

const (
	KindMessage    Kind = "message"
	KindThought    Kind = "thought"
	KindToolCall   Kind = "tool_call"
	KindToolOutput Kind = "tool_output"
	KindPlan       Kind = "plan"
	KindStage      Kind = "stage"
	KindTruncated  Kind = "truncated"
)

// Valid reports whether k is one of the kinds a transcript stores.
func (k Kind) Valid() bool {
	switch k {
	case KindMessage, KindThought, KindToolCall, KindToolOutput, KindPlan, KindStage, KindTruncated:
		return true
	}
	return false
}

// Limits on one entry and on one Run's transcript.
const (
	MaxTextBytes  = 4 << 10
	MaxInputBytes = 512
	MaxTitleBytes = 256
	MaxIDBytes    = 128
	MaxPaths      = 16
	MaxPathBytes  = 256
	MaxPlanSteps  = 32
	MaxStepBytes  = 256

	MaxRunEntries   = 5000
	MaxRunBytes     = 2 << 20
	MaxBatchEntries = 100
	MaxSeq          = 1 << 30
)

// Entry is one transcript entry. Seq orders a Run's entries and is assigned
// by the worker; At is when the worker observed the entry.
type Entry struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"at"`
	Kind       Kind      `json:"kind"`
	Text       string    `json:"text,omitempty"`
	ToolCallID string    `json:"toolCallId,omitempty"`
	Title      string    `json:"title,omitempty"`
	ToolKind   string    `json:"toolKind,omitempty"`
	Status     string    `json:"status,omitempty"`
	Input      string    `json:"input,omitempty"`
	Paths      []string  `json:"paths,omitempty"`
	Plan       []Step    `json:"plan,omitempty"`
	Clipped    bool      `json:"clipped,omitempty"`
}

// Step is one entry of an agent's plan.
type Step struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

var (
	toolKinds    = []string{"read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"}
	toolStatuses = []string{"pending", "in_progress", "completed", "failed"}
	stepStatuses = []string{"pending", "in_progress", "completed"}
)

// Clamp cuts every field of e to its limit and maps a tool kind or status
// outside the published enums to other, none and pending. Clipped is set
// when the text or the input summary lost bytes.
func Clamp(e Entry) Entry {
	if e.ToolKind != "" && !slices.Contains(toolKinds, e.ToolKind) {
		e.ToolKind = "other"
	}
	if !slices.Contains(toolStatuses, e.Status) {
		e.Status = ""
	}
	var cut bool
	e.Text, cut = clip(e.Text, MaxTextBytes)
	e.Clipped = e.Clipped || cut
	e.Input, cut = clip(e.Input, MaxInputBytes)
	e.Clipped = e.Clipped || cut
	e.Title, _ = clip(e.Title, MaxTitleBytes)
	e.ToolCallID, _ = clip(e.ToolCallID, MaxIDBytes)
	if len(e.Paths) > MaxPaths {
		e.Paths = e.Paths[:MaxPaths]
	}
	if len(e.Paths) > 0 {
		paths := make([]string, len(e.Paths))
		for i, p := range e.Paths {
			paths[i], _ = clip(p, MaxPathBytes)
		}
		e.Paths = paths
	}
	if len(e.Plan) > MaxPlanSteps {
		e.Plan = e.Plan[:MaxPlanSteps]
	}
	if len(e.Plan) > 0 {
		steps := make([]Step, len(e.Plan))
		for i, s := range e.Plan {
			steps[i].Content, _ = clip(s.Content, MaxStepBytes)
			steps[i].Status = s.Status
			if !slices.Contains(stepStatuses, s.Status) {
				steps[i].Status = "pending"
			}
		}
		e.Plan = steps
	}
	return e
}

// Size is the number of bytes e counts against a Run's byte limit: its JSON
// encoding.
func Size(e Entry) int {
	b, err := json.Marshal(e)
	if err != nil {
		return MaxRunBytes
	}
	return len(b)
}

// Marker is the single truncation entry a Run's transcript ends in. It takes
// the place of the entry at seq that would leave no room for it.
func Marker(seq int64, at time.Time) Entry {
	return Entry{Seq: seq, At: at, Kind: KindTruncated, Text: "The transcript reached its limit of " +
		strconv.Itoa(MaxRunEntries) + " entries or " + strconv.Itoa(MaxRunBytes>>20) + " MiB; later activity is not recorded."}
}

var largestMarkerBytes = Size(Marker(MaxSeq, time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -(23*3600+59*60)))))

// Fits reports whether an entry of size bytes may join a transcript that
// holds entries entries and bytes bytes. It fits only when a Marker still
// fits after it, so a transcript with its Marker never exceeds MaxRunEntries
// or MaxRunBytes.
func Fits(entries, bytes, size int) bool {
	return entries+2 <= MaxRunEntries && bytes+size+largestMarkerBytes <= MaxRunBytes
}

// clip cuts s to at most limit bytes without splitting a UTF-8 sequence, and
// reports whether it cut anything. Invalid UTF-8 is replaced first.
func clip(s string, limit int) (string, bool) {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

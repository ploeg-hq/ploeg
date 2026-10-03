package acp

import (
	"testing"
	"time"
)

func std() []PermissionOption {
	return []PermissionOption{
		{ID: "a1", Name: "Allow once", Kind: "allow_once"},
		{ID: "a2", Name: "Always allow", Kind: "allow_always"},
		{ID: "r1", Name: "Reject", Kind: "reject_once"},
	}
}

func TestPermission_ModeMatrix(t *testing.T) {
	tests := []struct {
		name string
		mode PermissionMode
		kind ToolKind
		want string
	}{
		{"allow-all grants an edit", PermissionAllowAll, ToolEdit, "a1"},
		{"allow-all grants a read", PermissionAllowAll, ToolRead, "a1"},
		{"read-only grants a read", PermissionReadOnly, ToolRead, "a1"},
		{"read-only grants a search", PermissionReadOnly, ToolSearch, "a1"},
		{"read-only REJECTS an edit", PermissionReadOnly, ToolEdit, "r1"},
		{"read-only REJECTS a delete", PermissionReadOnly, ToolDelete, "r1"},
		{"read-only rejects execute", PermissionReadOnly, ToolExecute, "r1"},
		{"deny-all rejects a read", PermissionDenyAll, ToolRead, "r1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPermissionPolicy(tc.mode, 0, 0)
			got := p.Decide(PermissionRequest{ToolKind: tc.kind, Options: std()})
			if got.Storm {
				t.Fatal("unexpected storm")
			}
			if got.OptionID != tc.want {
				t.Errorf("optionId = %q, want %q", got.OptionID, tc.want)
			}
		})
	}
}

// allow_once before allow_always: a grant should never widen beyond the call
// that asked for it.
func TestPermission_PrefersOnceOverAlways(t *testing.T) {
	p := NewPermissionPolicy(PermissionAllowAll, 0, 0)
	got := p.Decide(PermissionRequest{ToolKind: ToolEdit, Options: []PermissionOption{
		{ID: "always", Kind: "allow_always"},
		{ID: "once", Kind: "allow_once"},
	}})
	if got.OptionID != "once" {
		t.Errorf("optionId = %q, want once", got.OptionID)
	}
}

// Agents ship non-standard option sets; the heuristic covers them without ever
// guessing "the first option", which on a deny decision would grant a mutation.
func TestPermission_HeuristicFallback(t *testing.T) {
	p := NewPermissionPolicy(PermissionAllowAll, 0, 0)
	got := p.Decide(PermissionRequest{ToolKind: ToolEdit, Options: []PermissionOption{
		{ID: "opt-1", Name: "Nope"},
		{ID: "opt-2", Name: "Yes, proceed"},
	}})
	if got.OptionID != "opt-2" {
		t.Errorf("optionId = %q, want opt-2", got.OptionID)
	}
}

func TestPermission_DisallowIsNotAnAllow(t *testing.T) {
	p := NewPermissionPolicy(PermissionAllowAll, 0, 0)
	got := p.Decide(PermissionRequest{ToolKind: ToolEdit, Options: []PermissionOption{
		{ID: "bad", Name: "Disallow this tool"},
	}})
	if got.OptionID == "bad" {
		t.Error(`"Disallow" was matched as an allow — substring trap`)
	}
}

func TestPermission_NoAcceptableOptionYieldsNoGuess(t *testing.T) {
	p := NewPermissionPolicy(PermissionDenyAll, 0, 0)
	got := p.Decide(PermissionRequest{ToolKind: ToolEdit, Options: []PermissionOption{
		{ID: "a1", Kind: "allow_once"}, // only an allow offered, but we must deny
	}})
	if got.OptionID != "" {
		t.Errorf("optionId = %q, want empty (answer cancelled, never guess)", got.OptionID)
	}
}

func TestPermission_TotalCapTripsAStorm(t *testing.T) {
	p := NewPermissionPolicy(PermissionAllowAll, 3, 1000)
	for i := 0; i < 3; i++ {
		if d := p.Decide(PermissionRequest{ToolKind: ToolRead, Options: std()}); d.Storm {
			t.Fatalf("storm at request %d, cap is 3", i+1)
		}
	}
	if d := p.Decide(PermissionRequest{ToolKind: ToolRead, Options: std()}); !d.Storm {
		t.Error("4th request did not trip the total cap")
	}
}

func TestPermission_RateCapTripsAndWindowSlides(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	p := NewPermissionPolicy(PermissionAllowAll, 1000, 2)
	p.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if d := p.Decide(PermissionRequest{ToolKind: ToolRead, Options: std()}); d.Storm {
			t.Fatalf("storm at request %d, rate cap is 2/min", i+1)
		}
	}
	if d := p.Decide(PermissionRequest{ToolKind: ToolRead, Options: std()}); !d.Storm {
		t.Fatal("3rd request in the same minute did not trip the rate cap")
	}
	// A steady, slow asker is not a storm: slide past the window.
	now = now.Add(2 * time.Minute)
	if d := p.Decide(PermissionRequest{ToolKind: ToolRead, Options: std()}); d.Storm {
		t.Error("request after the window slid still reported a storm")
	}
}

func TestPermission_StatsNameTheWorstOffenders(t *testing.T) {
	p := NewPermissionPolicy(PermissionAllowAll, 0, 0)
	for i := 0; i < 5; i++ {
		p.Decide(PermissionRequest{ToolKind: ToolExecute, Title: "run npm install", Options: std()})
	}
	for i := 0; i < 2; i++ {
		p.Decide(PermissionRequest{ToolKind: ToolRead, Title: "read go.mod", Options: std()})
	}
	total, top := p.Stats()
	if total != 7 {
		t.Errorf("total = %d, want 7", total)
	}
	if len(top) == 0 || top[0] != "run npm install" {
		t.Errorf("topTitles = %v, want the most frequent first", top)
	}
}

func TestParsePermissionMode(t *testing.T) {
	for in, want := range map[string]PermissionMode{
		"allow_always": PermissionAllowAll, "allowAlways": PermissionAllowAll,
		"allow_read_only": PermissionReadOnly, "deny_all": PermissionDenyAll,
		"": PermissionAllowAll,
	} {
		got, ok := ParsePermissionMode(in)
		if !ok || got != want {
			t.Errorf("ParsePermissionMode(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}
	// An unknown mode must be reported, not silently treated as allow-all:
	// the caller fails startup rather than running wide open by accident.
	if got, ok := ParsePermissionMode("yolo"); ok {
		t.Errorf("ParsePermissionMode(\"yolo\") = %q,true; want ok=false", got)
	}
}

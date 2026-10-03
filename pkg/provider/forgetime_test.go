package provider

import (
	"testing"
	"time"
)

func TestParseForgeTime(t *testing.T) {
	want := time.Date(2026, 10, 1, 7, 30, 0, 0, time.UTC)
	for _, s := range []string{"2026-10-01T07:30:00Z", "2026-10-01T09:30:00+02:00", "2026-10-01 07:30:00 UTC", "2026-10-01 09:30:00 +0200"} {
		got := ParseForgeTime(s)
		if got == nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("ParseForgeTime(%q) = %v, want %v in UTC", s, got, want)
		}
	}
	for _, s := range []string{"", "  ", "yesterday", "0"} {
		if got := ParseForgeTime(s); got != nil {
			t.Errorf("ParseForgeTime(%q) = %v, want nil (unknown)", s, got)
		}
	}
}

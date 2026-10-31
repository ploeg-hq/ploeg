package provider

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestParseStatusMapReadsEveryMappableState(t *testing.T) {
	got, err := ParseStatusMap(" awaiting_review = in review , needs_human=blocked,done=complete,")
	if err != nil {
		t.Fatal(err)
	}
	want := map[work.State]string{work.StateAwaitingReview: "in review", work.StateNeedsHuman: "blocked", work.StateDone: "complete"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseStatusMapRefusesWhatSetStatusNeverReceives(t *testing.T) {
	for _, in := range []string{"active=in progress", "queued=to do", "done", "done=", "reviewing=x", "done=a,done=b"} {
		if _, err := ParseStatusMap(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	if got, err := ParseStatusMap(""); err != nil || len(got) != 0 {
		t.Errorf("an empty map = %v, %v", got, err)
	}
}

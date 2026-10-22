package acp

import (
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func TestTheAdapterStatesTheLimitsItEnforces(t *testing.T) {
	a, err := New("opencode", ProfileOverrides{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var limiter harness.PromptLimiter = a
	if prompt, idle := limiter.PromptLimits(); prompt != 45*time.Minute || idle != 10*time.Minute {
		t.Fatalf("defaults = %s, %s; want 45m and 10m", prompt, idle)
	}
	a, err = New("opencode", ProfileOverrides{}, Options{PromptTimeout: time.Hour, IdleTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if prompt, idle := a.PromptLimits(); prompt != time.Hour || idle != 5*time.Minute {
		t.Fatalf("configured = %s, %s; want 1h and 5m", prompt, idle)
	}
}

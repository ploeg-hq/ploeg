package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestLoopDurationsRenderNothingBeforeTheFirstPass(t *testing.T) {
	var d LoopDurations
	if got := d.render(); len(got) != 0 {
		t.Fatalf("render = %q, want nothing", got)
	}
}

func TestLoopDurationsRenderTheLastPassAndTheTotalsPerLoop(t *testing.T) {
	var d LoopDurations
	d.Observe("expiry", 2*time.Second)
	d.Observe("expiry", 500*time.Millisecond)
	d.Observe("credentials", 3*time.Second)
	got := string(d.render())
	for _, want := range []string{
		"# TYPE ploeg_sweep_pass_last_duration_seconds gauge\n",
		`ploeg_sweep_pass_last_duration_seconds{loop="credentials"} 3` + "\n",
		`ploeg_sweep_pass_last_duration_seconds{loop="expiry"} 0.5` + "\n",
		"# TYPE ploeg_sweep_pass_duration_seconds_total counter\n",
		`ploeg_sweep_pass_duration_seconds_total{loop="expiry"} 2.5` + "\n",
		"# TYPE ploeg_sweep_passes_total counter\n",
		`ploeg_sweep_passes_total{loop="expiry"} 2` + "\n",
		`ploeg_sweep_passes_total{loop="credentials"} 1` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, `loop="credentials"`) > strings.Index(got, `loop="expiry"`) {
		t.Errorf("loops are not sorted by name:\n%s", got)
	}
}

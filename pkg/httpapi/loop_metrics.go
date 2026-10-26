package httpapi

import (
	"slices"
	"sync"
	"time"
)

// LoopDurations records, per background loop, how long its passes took. The
// zero value is ready to use and safe for concurrent use.
type LoopDurations struct {
	mu    sync.Mutex
	loops map[string]loopDuration
}

type loopDuration struct {
	last   time.Duration
	total  time.Duration
	passes int
}

// Observe records one finished pass of the named loop.
func (d *LoopDurations) Observe(loop string, took time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loops == nil {
		d.loops = map[string]loopDuration{}
	}
	l := d.loops[loop]
	l.last = took
	l.total += took
	l.passes++
	d.loops[loop] = l
}

func (d *LoopDurations) render() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.loops) == 0 {
		return nil
	}
	names := make([]string, 0, len(d.loops))
	for name := range d.loops {
		names = append(names, name)
	}
	slices.Sort(names)
	var b metricWriter
	b.family("ploeg_sweep_pass_last_duration_seconds", "gauge", "How long the last finished pass of each background sweep loop took.")
	for _, name := range names {
		b.sample("ploeg_sweep_pass_last_duration_seconds", d.loops[name].last.Seconds(), "loop", name)
	}
	b.family("ploeg_sweep_pass_duration_seconds_total", "counter", "Time spent in finished passes of each background sweep loop.")
	for _, name := range names {
		b.sample("ploeg_sweep_pass_duration_seconds_total", d.loops[name].total.Seconds(), "loop", name)
	}
	b.family("ploeg_sweep_passes_total", "counter", "Finished passes of each background sweep loop.")
	for _, name := range names {
		b.sample("ploeg_sweep_passes_total", float64(d.loops[name].passes), "loop", name)
	}
	return b.Bytes()
}

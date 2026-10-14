package worker

import (
	"sync"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type observedAgent struct {
	mu    sync.Mutex
	agent *harness.HarnessAgent
}

func (o *observedAgent) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.agent = nil
}

func (o *observedAgent) observe(agent harness.HarnessAgent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	agent.Name, agent.Version = cutUTF8(agent.Name, maxHarnessVersionBytes), cutUTF8(agent.Version, maxHarnessVersionBytes)
	if o.agent == nil && agent.Name != "" {
		o.agent = &agent
	}
}

func (o *observedAgent) get() *harness.HarnessAgent {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.agent == nil {
		return nil
	}
	agent := *o.agent
	return &agent
}

func (w *Worker) runHarness() *harness.RunHarness {
	h := harness.RunHarness{Name: w.API.Harness, Profile: w.API.HarnessProfile, Version: w.API.HarnessVersion, Agent: w.agent.get()}
	if h == (harness.RunHarness{}) {
		return nil
	}
	return &h
}

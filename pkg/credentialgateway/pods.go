package credentialgateway

import (
	"context"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
)

const podCacheFor = 5 * time.Second

type cachedPods struct {
	pods  []kubeauth.Pod
	until time.Time
}

type podCache struct {
	lookup    PodLookup
	namespace string
	selector  string
	now       func() time.Time

	mu      sync.Mutex
	entries map[string]cachedPods
}

func (c *podCache) at(ctx context.Context, ip string, fresh bool) ([]kubeauth.Pod, error) {
	now := c.now()
	if !fresh {
		c.mu.Lock()
		entry, hit := c.entries[ip]
		c.mu.Unlock()
		if hit && now.Before(entry.until) {
			return entry.pods, nil
		}
	}
	pods, err := c.lookup.PodsAt(ctx, c.namespace, c.selector, ip)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for k, v := range c.entries {
		if now.After(v.until) {
			delete(c.entries, k)
		}
	}
	c.entries[ip] = cachedPods{pods: pods, until: now.Add(podCacheFor)}
	c.mu.Unlock()
	return pods, nil
}

func (c *podCache) runningAt(ctx context.Context, ip string) (kubeauth.Pod, bool) {
	pods, err := c.at(ctx, ip, false)
	if err != nil || len(pods) != 1 {
		return kubeauth.Pod{}, false
	}
	p := pods[0]
	return p, p.UID != "" && p.IP == ip && p.Phase == "Running" && !p.Deleting
}

var runPodOwners = map[string]bool{"Job": true, "Sandbox": true}

func (c *podCache) claimant(ctx context.Context, ip string) (kubeauth.Pod, bool) {
	pods, err := c.at(ctx, ip, true)
	if err != nil || len(pods) != 1 {
		return kubeauth.Pod{}, false
	}
	p := pods[0]
	return p, p.IP == ip && p.Phase == "Running" && !p.Deleting && runPodOwners[p.OwnerKind]
}

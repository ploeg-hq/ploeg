package team

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Credential isolation modes of a pool. Only the modes in
// isolationOutsideHarness count as qualified: proxy is ADR-0034's
// placeholders with the worker keeping credentials. gateway names the
// credential gateway outside the Run pod (ADR-0197) and qualifies a pool
// only once that gateway ships.
const (
	IsolationProxy   = "proxy"
	IsolationGateway = "gateway"
)

var isolationOutsideHarness = []string{IsolationProxy}

// Pool is one generic worker pool the chart renders for runtime Teams of
// one Tenant on one Harness (ADR-0198 section 7).
type Pool struct {
	Tenant          string `json:"tenant"`
	Harness         string `json:"harness"`
	MaxReplicaCount int    `json:"maxReplicaCount"`
	Isolation       string `json:"isolation"`
}

// Qualified reports whether the pool's Harness runs with its credentials
// isolated from the Run.
func (p Pool) Qualified() bool {
	return slices.Contains(isolationOutsideHarness, p.Isolation)
}

// Pools are the pools ploegd learns from PLOEG_RUNTIME_POOLS.
type Pools []Pool

var poolPart = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,63}$`)

// ParsePools reads PLOEG_RUNTIME_POOLS. Empty is no pool.
func ParsePools(raw string) (Pools, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var pools Pools
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pools); err != nil {
		return nil, fmt.Errorf("PLOEG_RUNTIME_POOLS is a JSON array of tenant, harness, maxReplicaCount and isolation: %w", err)
	}
	for i, p := range pools {
		if !poolPart.MatchString(p.Tenant) || !poolPart.MatchString(p.Harness) || p.MaxReplicaCount < 1 {
			return nil, fmt.Errorf("PLOEG_RUNTIME_POOLS[%d]: needs a tenant, a harness and maxReplicaCount of 1 or more", i)
		}
		if slices.ContainsFunc(pools[:i], func(o Pool) bool { return o.Tenant == p.Tenant && o.Harness == p.Harness }) {
			return nil, fmt.Errorf("PLOEG_RUNTIME_POOLS[%d]: a second pool for tenant %s and harness %s", i, p.Tenant, p.Harness)
		}
	}
	return pools, nil
}

// For returns the pool of a Tenant and Harness.
func (ps Pools) For(tenant, harness string) (Pool, bool) {
	for _, p := range ps {
		if p.Tenant == tenant && p.Harness == harness {
			return p, true
		}
	}
	return Pool{}, false
}

// BootstrapKey is the pool's key in the worker bootstrap Secret.
func (p Pool) BootstrapKey() string {
	return "pool--" + p.Tenant + "--" + strings.ReplaceAll(p.Harness, "/", "-")
}

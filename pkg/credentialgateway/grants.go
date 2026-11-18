package credentialgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
)

const (
	grantCacheLimit  = 60 * time.Second
	grantRefreshSlop = 2 * time.Minute
)

var (
	errRunSettled         = errors.New("the Run has settled")
	errCapabilityRefused  = errors.New("ploegd refused the capability")
	errGatewayUnavailable = errors.New("credential_gateway_unavailable")
)

type ploegdClient struct {
	base      string
	tokenFile string
	http      *http.Client
}

func (c ploegdClient) gatewayToken() (string, error) {
	raw, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("read the gateway's projected token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func (c ploegdClient) post(ctx context.Context, path string, body any, out any) (int, error) {
	token, err := c.gatewayToken()
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gatewaycontract.AuthorizationHeader, "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

type cachedGrant struct {
	grant gatewaycontract.Grant
	until time.Time
}

type grantCache struct {
	ploegd ploegdClient
	now    func() time.Time

	mu      sync.Mutex
	entries map[[32]byte]cachedGrant
}

func grantKey(capability, sourceIP string) [32]byte {
	return sha256.Sum256([]byte(capability + "\x00" + sourceIP))
}

func (c *grantCache) get(ctx context.Context, capability, sourceIP string) (gatewaycontract.Grant, error) {
	key := grantKey(capability, sourceIP)
	now := c.now()
	c.mu.Lock()
	entry, hit := c.entries[key]
	c.mu.Unlock()
	if hit && now.Before(entry.until) {
		return entry.grant, nil
	}
	var grant gatewaycontract.Grant
	status, err := c.ploegd.post(ctx, gatewaycontract.GrantsPath, gatewaycontract.GrantRequest{Capability: capability, SourceIP: sourceIP}, &grant)
	switch {
	case err != nil:
		return grant, fmt.Errorf("%w: %v", errGatewayUnavailable, err)
	case status == http.StatusGone:
		c.forget(key)
		return grant, errRunSettled
	case status == http.StatusUnauthorized || status >= 500:
		return grant, fmt.Errorf("%w: ploegd answered %d", errGatewayUnavailable, status)
	case status != http.StatusOK:
		c.forget(key)
		return grant, errCapabilityRefused
	}
	until := now.Add(grantCacheLimit)
	if grant.ForgeTokenExpiresAt != nil {
		if expiry := grant.ForgeTokenExpiresAt.Add(-grantRefreshSlop); expiry.Before(until) {
			until = expiry
		}
	}
	if !grant.Deadline.IsZero() && grant.Deadline.Before(until) {
		until = grant.Deadline
	}
	c.mu.Lock()
	for k, v := range c.entries {
		if now.After(v.until) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cachedGrant{grant: grant, until: until}
	c.mu.Unlock()
	return grant, nil
}

func (c *grantCache) forget(key [32]byte) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

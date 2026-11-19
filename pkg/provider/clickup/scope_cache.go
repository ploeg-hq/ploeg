package clickup

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Probing a List for a runtime binding reads the List, its members and the
// Workspace's webhooks, and the webhook check right after reads the List and
// its members again. These short-lived caches let the second read reuse the
// first, and let a name resolve once, so a binding costs a handful of calls
// out of the token's budget that ingest shares (ADR-0186).
const (
	listCacheTTL = time.Minute
	nameCacheTTL = 5 * time.Minute
)

type listStatus struct {
	Status string `json:"status"`
}

type listBody struct {
	ID              flexID `json:"id"`
	Name            string `json:"name"`
	PermissionLevel string `json:"permission_level"`
	Folder          struct {
		ID flexID `json:"id"`
	} `json:"folder"`
	Space struct {
		ID flexID `json:"id"`
	} `json:"space"`
	Statuses []listStatus `json:"statuses"`
}

type listMember struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

type cached[T any] struct {
	value T
	until time.Time
}

type scopeCache struct {
	mu      sync.Mutex
	lists   map[string]cached[listBody]
	members map[string]cached[[]listMember]
	names   map[string]cached[string]
}

func (p *Provider) scopeCache() *scopeCache {
	if c := p.scopes.Load(); c != nil {
		return c
	}
	p.scopes.CompareAndSwap(nil, &scopeCache{lists: map[string]cached[listBody]{}, members: map[string]cached[[]listMember]{}, names: map[string]cached[string]{}})
	return p.scopes.Load()
}

func lookup[T any](c *scopeCache, m map[string]cached[T], key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := m[key]
	if !ok || time.Now().After(hit.until) {
		var zero T
		return zero, false
	}
	return hit.value, true
}

func keep[T any](c *scopeCache, m map[string]cached[T], key string, value T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m[key] = cached[T]{value: value, until: time.Now().Add(ttl)}
}

func (p *Provider) readList(ctx context.Context, listID string) (listBody, error) {
	c := p.scopeCache()
	if hit, ok := lookup(c, c.lists, listID); ok {
		return hit, nil
	}
	var body listBody
	if err := p.do(ctx, http.MethodGet, "/list/"+url.PathEscape(listID), nil, &body); err != nil {
		return body, err
	}
	if body.ID != "" {
		keep(c, c.lists, listID, body, listCacheTTL)
	}
	return body, nil
}

func (p *Provider) readMembers(ctx context.Context, listID string) ([]listMember, error) {
	c := p.scopeCache()
	if hit, ok := lookup(c, c.members, listID); ok {
		return hit, nil
	}
	var body struct {
		Members []listMember `json:"members"`
	}
	if err := p.do(ctx, http.MethodGet, "/list/"+url.PathEscape(listID)+"/member", nil, &body); err != nil {
		return nil, err
	}
	keep(c, c.members, listID, body.Members, listCacheTTL)
	return body.Members, nil
}

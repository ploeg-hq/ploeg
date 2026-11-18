package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/untrusted"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// How long a tracker read is reused before Ploeg asks the tracker again
// (ADR-0196). A failed read is reused for failedReadTTL, so a consumer that
// retries at once does not reach the tracker again.
const (
	scopesReadTTL     = 60 * time.Second
	scopeItemsReadTTL = 30 * time.Second
	itemReadTTL       = 15 * time.Second
	failedReadTTL     = 10 * time.Second
	trackerReadLimit  = 1024
	trackerReadWait   = 20 * time.Second
	maxTrackerID      = 256
	maxTrackerTitle   = 4096
	maxTrackerText    = 16384
	maxTrackerLabels  = 100
)

var trackerTextRedactor = transcript.NewRedactor()

type trackerReadEntry struct {
	value     any
	err       error
	fetchedAt time.Time
	expires   time.Time
}

type trackerReadCache struct {
	mu      sync.Mutex
	entries map[string]trackerReadEntry
	now     func() time.Time
}

func (c *trackerReadCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *trackerReadCache) read(key string, ttl time.Duration, load func() (any, error)) (any, time.Time, error) {
	c.mu.Lock()
	now := c.clock()
	if e, ok := c.entries[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.value, e.fetchedAt, e.err
	}
	c.mu.Unlock()
	value, err := load()
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.clock()
	if c.entries == nil {
		c.entries = map[string]trackerReadEntry{}
	}
	if len(c.entries) >= trackerReadLimit {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= trackerReadLimit {
			clear(c.entries)
		}
	}
	keep := ttl
	if err != nil {
		keep = failedReadTTL
	}
	c.entries[key] = trackerReadEntry{value: value, err: err, fetchedAt: now, expires: now.Add(keep)}
	return value, now, err
}

func (s *Server) registerTrackerReads(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/tracker-connections/{connection}/scopes", s.handleTrackerScopes)
	mux.HandleFunc("GET /api/v1/operator/tracker-connections/{connection}/scopes/{scope}/items", s.handleTrackerScopeItems)
	mux.HandleFunc("GET /api/v1/operator/tracker-connections/{connection}/items/{item}", s.handleTrackerItem)
}

func (s *Server) trackerConnection(w http.ResponseWriter, r *http.Request) (string, provider.TrackerProvider, bool) {
	principal, _ := OperatorPrincipalFromContext(r.Context())
	if len(principal.ReadTrackers) == 0 {
		operatorError(w, 403, "permission_required", "Reading tracker Scopes and items needs readTrackers.")
		return "", nil, false
	}
	connection := r.PathValue("connection")
	tracker, ok := s.Trackers[connection]
	if !operatorName.MatchString(connection) || !principal.CanReadTracker(connection) || !ok || tracker == nil {
		operatorError(w, 404, "unknown_connection", "No tracker connection by that id is readable by this consumer.")
		return "", nil, false
	}
	return connection, tracker, true
}

func trackerID(raw string) (string, bool) {
	if raw == "" || len(raw) > maxTrackerID || !utf8.ValidString(raw) {
		return "", false
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
	}
	return raw, true
}

func trackerQuery(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for key, values := range r.URL.Query() {
		known := false
		for _, a := range allowed {
			known = known || a == key
		}
		if !known || len(values) != 1 {
			operatorError(w, 400, "invalid_request", "Unknown or repeated query parameter: "+key+".")
			return false
		}
	}
	return true
}

func trackerReadFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, provider.ErrNotFound) {
		operatorError(w, 404, "not_found", "The tracker has no such Scope or item, or the connection cannot see it.")
		return
	}
	operatorError(w, 502, "tracker_unavailable", "The tracker did not answer the read; try again later.")
}

func trackerText(text string, limit int) string {
	text = untrusted.Escape(trackerTextRedactor.String(strings.ToValidUTF8(text, "�")))
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func trackerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxTrackerTitle || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	u.RawQuery, u.Fragment, u.ForceQuery = "", "", false
	return u.String()
}

type trackerScopeRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type trackerScopeName struct {
	Name string `json:"name"`
}

type trackerScope struct {
	Kind      string           `json:"kind"`
	ID        string           `json:"id"`
	Ref       string           `json:"ref"`
	Bindable  bool             `json:"bindable"`
	Browsable bool             `json:"browsable"`
	Untrusted trackerScopeName `json:"untrusted"`
}

type trackerScopesResponse struct {
	SchemaVersion string           `json:"schemaVersion"`
	Connection    string           `json:"connection"`
	Parent        *trackerScopeRef `json:"parent"`
	Scopes        []trackerScope   `json:"scopes"`
	NextCursor    *string          `json:"nextCursor"`
	FetchedAt     time.Time        `json:"fetchedAt"`
}

func (s *Server) handleTrackerScopes(w http.ResponseWriter, r *http.Request) {
	connection, tracker, ok := s.trackerConnection(w, r)
	if !ok || !trackerQuery(w, r, "parent", "after", "limit") {
		return
	}
	lister, ok := tracker.(provider.ScopeLister)
	if !ok {
		operatorError(w, 422, "capability_missing", "This tracker connection cannot list its Scopes.")
		return
	}
	query := r.URL.Query()
	var parent provider.Scope
	var parentRef *trackerScopeRef
	if raw := query.Get("parent"); raw != "" {
		kind, id, found := strings.Cut(raw, ":")
		id, valid := trackerID(id)
		if !found || !operatorName.MatchString(kind) || !valid {
			operatorError(w, 400, "invalid_request", "parent is <kind>:<id>, as a listed Scope's ref names it.")
			return
		}
		parent = provider.Scope{Kind: kind, ID: id}
		parentRef = &trackerScopeRef{Kind: kind, ID: id}
	}
	offset, limit, ok := trackerPaging(w, query.Get("after"), query.Get("limit"))
	if !ok {
		return
	}
	key := "scopes\x00" + connection + "\x00" + parent.Kind + "\x00" + parent.ID
	value, fetchedAt, err := s.trackerReads.read(key, scopesReadTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), trackerReadWait)
		defer cancel()
		return lister.ListScopes(ctx, parent)
	})
	if err != nil {
		trackerReadFailed(w, err)
		return
	}
	nodes, _ := value.([]provider.ScopeNode)
	nodes = nodes[:min(len(nodes), provider.MaxScopeChildren)]
	out := trackerScopesResponse{SchemaVersion: "1.0", Connection: connection, Parent: parentRef, Scopes: []trackerScope{}, FetchedAt: fetchedAt.UTC()}
	if offset < len(nodes) {
		end := min(offset+limit, len(nodes))
		for _, n := range nodes[offset:end] {
			id, valid := trackerID(n.ID)
			if !valid || !operatorName.MatchString(n.Kind) {
				continue
			}
			out.Scopes = append(out.Scopes, trackerScope{Kind: n.Kind, ID: id, Ref: n.Kind + ":" + id, Bindable: n.Bindable, Browsable: n.Browsable,
				Untrusted: trackerScopeName{Name: trackerText(n.Name, maxTrackerTitle)}})
		}
		if end < len(nodes) {
			next := strconv.Itoa(end)
			out.NextCursor = &next
		}
	}
	operatorJSON(w, 200, out)
}

func trackerPaging(w http.ResponseWriter, after, rawLimit string) (int, int, bool) {
	offset, limit := 0, 50
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil || n < 0 || n > provider.MaxScopeChildren {
			operatorError(w, 400, "invalid_request", "after is the nextCursor of the previous page.")
			return 0, 0, false
		}
		offset = n
	}
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 || n > 200 {
			operatorError(w, 400, "invalid_request", "limit is between 1 and 200.")
			return 0, 0, false
		}
		limit = n
	}
	return offset, limit, true
}

type trackerItemText struct {
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
}

type trackerScopeItem struct {
	ID        string          `json:"id"`
	URL       string          `json:"url"`
	UpdatedAt *time.Time      `json:"updatedAt"`
	Untrusted trackerItemText `json:"untrusted"`
}

type trackerScopeItemsResponse struct {
	SchemaVersion string             `json:"schemaVersion"`
	Connection    string             `json:"connection"`
	Scope         string             `json:"scope"`
	Items         []trackerScopeItem `json:"items"`
	NextCursor    *string            `json:"nextCursor"`
	FetchedAt     time.Time          `json:"fetchedAt"`
}

func (s *Server) handleTrackerScopeItems(w http.ResponseWriter, r *http.Request) {
	connection, tracker, ok := s.trackerConnection(w, r)
	if !ok || !trackerQuery(w, r, "after") {
		return
	}
	lister, ok := tracker.(provider.ScopeItemLister)
	if !ok {
		operatorError(w, 422, "capability_missing", "This tracker connection cannot list a Scope's open items.")
		return
	}
	scope, valid := trackerID(r.PathValue("scope"))
	if !valid {
		operatorError(w, 400, "invalid_request", "The Scope id is empty, too long or not printable.")
		return
	}
	page := 1
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > 10000 {
			operatorError(w, 400, "invalid_request", "after is the nextCursor of the previous page.")
			return
		}
		page = n
	}
	key := "items\x00" + connection + "\x00" + scope + "\x00" + strconv.Itoa(page)
	value, fetchedAt, err := s.trackerReads.read(key, scopeItemsReadTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), trackerReadWait)
		defer cancel()
		return lister.ListOpenItems(ctx, scope, page)
	})
	if err != nil {
		trackerReadFailed(w, err)
		return
	}
	result, _ := value.(provider.ScopeItemPage)
	out := trackerScopeItemsResponse{SchemaVersion: "1.0", Connection: connection, Scope: scope, Items: []trackerScopeItem{}, FetchedAt: fetchedAt.UTC()}
	for _, item := range result.Items[:min(len(result.Items), 100)] {
		id, valid := trackerID(item.ID)
		if !valid {
			continue
		}
		entry := trackerScopeItem{ID: id, URL: trackerURL(item.URL), Untrusted: trackerItemText{Title: trackerText(item.Title, maxTrackerTitle), Status: trackerText(item.Status, maxTrackerTitle)}}
		if !item.UpdatedAt.IsZero() {
			at := item.UpdatedAt.UTC()
			entry.UpdatedAt = &at
		}
		out.Items = append(out.Items, entry)
	}
	if result.NextPage > page {
		next := strconv.Itoa(result.NextPage)
		out.NextCursor = &next
	}
	operatorJSON(w, 200, out)
}

type trackerItemUntrusted struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

type trackerItem struct {
	ID        string               `json:"id"`
	Scope     string               `json:"scope"`
	URL       string               `json:"url"`
	Closed    bool                 `json:"closed"`
	CreatedAt *time.Time           `json:"createdAt"`
	Untrusted trackerItemUntrusted `json:"untrusted"`
}

type trackerItemResponse struct {
	SchemaVersion string      `json:"schemaVersion"`
	Connection    string      `json:"connection"`
	Item          trackerItem `json:"item"`
	FetchedAt     time.Time   `json:"fetchedAt"`
}

func (s *Server) handleTrackerItem(w http.ResponseWriter, r *http.Request) {
	connection, tracker, ok := s.trackerConnection(w, r)
	if !ok || !trackerQuery(w, r) {
		return
	}
	id, valid := trackerID(r.PathValue("item"))
	if !valid {
		operatorError(w, 400, "invalid_request", "The item id is empty, too long or not printable.")
		return
	}
	key := "item\x00" + connection + "\x00" + id
	value, fetchedAt, err := s.trackerReads.read(key, itemReadTTL, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), trackerReadWait)
		defer cancel()
		return tracker.FetchItem(ctx, id)
	})
	if err != nil {
		trackerReadFailed(w, err)
		return
	}
	item, _ := value.(work.WorkItem)
	itemID, valid := trackerID(item.ExternalID)
	if !valid {
		itemID = id
	}
	out := trackerItemResponse{SchemaVersion: "1.0", Connection: connection, FetchedAt: fetchedAt.UTC(), Item: trackerItem{
		ID: itemID, URL: trackerURL(item.URL), Closed: item.Closed,
		Untrusted: trackerItemUntrusted{Title: trackerText(item.Title, maxTrackerTitle), Description: trackerText(item.Description, maxTrackerText), Labels: []string{}},
	}}
	if scope, valid := trackerID(item.ExternalScope); valid {
		out.Item.Scope = scope
	}
	for _, label := range item.Labels[:min(len(item.Labels), maxTrackerLabels)] {
		out.Item.Untrusted.Labels = append(out.Item.Untrusted.Labels, trackerText(label, maxTrackerTitle))
	}
	if !item.TrackerCreatedAt.IsZero() {
		at := item.TrackerCreatedAt.UTC()
		out.Item.CreatedAt = &at
	}
	operatorJSON(w, 200, out)
}

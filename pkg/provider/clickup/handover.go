package clickup

import (
	"context"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Connection is a store-backed ClickUp connection (ADR-0195) as intake and
// write-back reach it. Token is the connection's own credential from the
// secret store; it is never logged.
type Connection struct {
	ID string
	// Agent is the ClickUp user id of the connection's own account when the
	// connection has handOverAgent set (ADR-0195 section 9), else empty.
	Agent   string
	Token   string
	BaseURL string
	// ScopeStatuses returns the status names the binding of a List through
	// this connection overrides Statuses with.
	ScopeStatuses func(listID string) map[work.State]string
}

// Connections finds the store-backed connections a delivery or a
// write-back goes through. ploegd builds it from the connection store, the
// runtime tracker bindings and the Work Items Ploeg holds.
type Connections interface {
	// HandOverAgents returns the active connections whose own account, by
	// ClickUp user id, is a hand-over agent.
	HandOverAgents(ctx context.Context, userID string) []Connection
	// BoundThrough returns the active store-backed connection through which
	// a routed runtime binding binds the List (ADR-0186). A List a static
	// route covers has no routed binding, so static routing wins.
	BoundThrough(ctx context.Context, listID string) (Connection, bool)
	// TaskList returns the List Ploeg recorded for a task it holds.
	TaskList(ctx context.Context, taskID string) (string, bool)
}

type handedOver struct {
	who     string
	removed bool
}

// handOver finds an assignee change of a connection's own hand-over account
// on a List bound through that same connection. The task is read with the
// connection's credential, because only it is known to see the List. A
// match is remembered so the task's first mirror and write-backs go through
// the connection before its Work Item exists.
func (p *Provider) handOver(ctx context.Context, pl hookPayload) (handedOver, bool) {
	if p.Connections == nil {
		return handedOver{}, false
	}
	for _, h := range pl.HistoryItems {
		if h.Field != "assignee" && h.Field != "assignee_add" && h.Field != "assignee_rem" {
			continue
		}
		member := h.After
		if member.ID == "" {
			member = h.Before
		}
		if member.ID == "" {
			continue
		}
		for _, c := range p.Connections.HandOverAgents(ctx, member.ID) {
			if c.Agent != member.ID {
				continue
			}
			t, err := p.through(c).readTask(ctx, pl.TaskID)
			if err != nil || t.List.ID == "" {
				p.log().Warn("clickup hand-over assignment not read through its connection", "task", pl.TaskID, "connection", c.ID, "err", err)
				continue
			}
			bound, ok := p.Connections.BoundThrough(ctx, t.List.ID)
			if !ok || bound.ID != c.ID {
				p.log().Debug("clickup hand-over assignment ignored: the List is not bound through the agent's connection", "task", pl.TaskID, "connection", c.ID, "list", t.List.ID)
				continue
			}
			p.handed.Store(pl.TaskID, t.List.ID)
			who := firstNonEmpty(member.Username, member.Email, member.ID)
			return handedOver{who: who, removed: h.Field == "assignee_rem"}, true
		}
	}
	return handedOver{}, false
}

// via returns the provider a task's reads and write-backs go through: the
// store-backed connection its List is bound through at runtime, else p with
// the static credential. The List is the one Ploeg recorded for the task,
// or before its Work Item exists the one a hand-over read found.
func (p *Provider) via(ctx context.Context, taskID string) *Provider {
	if p.Connections == nil || taskID == "" {
		return p
	}
	list, ok := p.Connections.TaskList(ctx, taskID)
	if !ok {
		found, seen := p.handed.Load(taskID)
		if !seen {
			return p
		}
		list = found.(string)
	}
	if c, bound := p.Connections.BoundThrough(ctx, list); bound {
		return p.through(c)
	}
	return p
}

// through is p acting with a store-backed connection's credential, keeping
// p's routing and status settings.
func (p *Provider) through(c Connection) *Provider {
	statuses := p.ScopeStatuses
	if c.ScopeStatuses != nil {
		statuses = c.ScopeStatuses
	}
	return &Provider{DefaultTeam: p.DefaultTeam, TeamMap: p.TeamMap, BaseURL: c.BaseURL, Token: c.Token, DoneStatus: p.DoneStatus,
		Statuses: p.Statuses, ScopeStatuses: statuses, HC: p.HC, Log: p.Log}
}

// ConnectionBaseURL is the API root a store-backed connection of inst reads
// and writes through: the instance's apiUrl with ClickUp's /v2, as the
// connection probe uses it.
func ConnectionBaseURL(inst provider.ConnectionInstance) string {
	return apiURL(inst) + "/v2"
}

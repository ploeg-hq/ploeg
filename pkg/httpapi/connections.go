package httpapi

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// connectionEventWindowDays bounds how far back the connections view looks
// for a repository's latest forge webhook event.
const connectionEventWindowDays = 30

// connectionReadinessTimeout bounds the forge reads one connections request
// may make for repositories that were not ready when last inspected.
const connectionReadinessTimeout = 10 * time.Second

// Board webhook states in the connections view.
const (
	BoardWebhookPresent    = "present"
	BoardWebhookMissing    = "missing"
	BoardWebhookUnreadable = "unreadable"
	BoardWebhookUnresolved = "unresolved"
	BoardWebhookUnchecked  = "unchecked"
)

// ConnectionRepository is one repository Ploeg may route work to: a
// registered target, or the repository a board names directly.
type ConnectionRepository struct {
	// Key identifies the repository in the view and in Readiness. It is the
	// target key for a registered target and "owner/name@branch" otherwise.
	Key string
	// Label is the registered target key a repo/<key> label selects, or
	// empty when only a board's own rule reaches the repository.
	Label  string
	Target work.Target
}

// ConnectionBoard is one configured tracker board and how it routes.
type ConnectionBoard struct {
	Tracker string
	Name    string
	// ID is the pinned tracker id; empty when the board is resolved by name.
	ID   string
	Team string
	// Repository is the Key of the repository the board names directly.
	Repository string
	Default    string
	Allow      []string
}

// BoardWiring is what the latest tracker check found on one board.
type BoardWiring struct {
	ProjectID string
	Webhook   string
	// Shared lists the usernames the board is shared with directly. Nil when
	// the share list could not be read.
	Shared []string
}

// Connections answers GET /api/v1/operator/connections: every board and
// repository Ploeg routes to, and whether each is wired.
type Connections struct {
	Boards       []ConnectionBoard
	Repositories []ConnectionRepository
	// Readiness gates every repository in Repositories by its Key.
	Readiness *target.Gate

	mu        sync.Mutex
	wiring    map[string]BoardWiring
	checkedAt time.Time
}

// RecordBoards replaces the tracker wiring with a completed check, keyed by
// board name and, for a pinned board, by its id.
func (c *Connections) RecordBoards(wiring map[string]BoardWiring, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wiring = wiring
	c.checkedAt = at
}

// RefreshReadiness inspects every repository again.
func (c *Connections) RefreshReadiness(ctx context.Context) {
	if c.Readiness != nil {
		c.Readiness.Load(ctx)
	}
}

// Wiring returns what the latest tracker check found on a board, when that
// check finished, and whether the board was checked.
func (c *Connections) Wiring(b ConnectionBoard) (BoardWiring, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checkedAt.IsZero() {
		return BoardWiring{}, time.Time{}, false
	}
	key := b.Name
	if b.ID != "" {
		key = "#" + b.ID
	}
	w, ok := c.wiring[key]
	return w, c.checkedAt, ok
}

type connectionsResponse struct {
	SchemaVersion string                   `json:"schemaVersion"`
	GeneratedAt   time.Time                `json:"generatedAt"`
	CheckedAt     *time.Time               `json:"checkedAt"`
	EventWindow   int                      `json:"eventWindowDays"`
	Repositories  []connectionRepositoryV1 `json:"repositories"`
	Boards        []connectionBoardV1      `json:"boards"`
}

type connectionRepositoryV1 struct {
	Key             string     `json:"key"`
	Label           *string    `json:"label"`
	Forge           string     `json:"forge"`
	Owner           string     `json:"owner"`
	Repo            string     `json:"repo"`
	BaseBranch      string     `json:"baseBranch"`
	Readiness       string     `json:"readiness"`
	Reason          string     `json:"reason"`
	LastForgeEvent  *time.Time `json:"lastForgeEventAt"`
	BoardsRoutingTo []string   `json:"boards"`
}

type connectionBoardV1 struct {
	Tracker    string             `json:"tracker"`
	Name       string             `json:"name"`
	ProjectID  *string            `json:"projectId"`
	Team       *string            `json:"team"`
	Repository *string            `json:"repository"`
	Default    *string            `json:"default"`
	Allow      []string           `json:"allow"`
	Webhook    string             `json:"webhook"`
	Teams      []connectionTeamV1 `json:"teams"`
}

type connectionTeamV1 struct {
	Team      string                 `json:"team"`
	Assignees []connectionAssigneeV1 `json:"assignees"`
}

type connectionAssigneeV1 struct {
	Username string `json:"username"`
	Shared   *bool  `json:"shared"`
}

func (s *Server) handleOperatorConnections(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Connections does not accept query parameters.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	now := time.Now().UTC()
	body := connectionsResponse{SchemaVersion: "1.0", GeneratedAt: now, EventWindow: connectionEventWindowDays,
		Repositories: []connectionRepositoryV1{}, Boards: []connectionBoardV1{}}
	c := s.Connections
	if c == nil {
		operatorJSON(w, 200, body)
		return
	}
	events, err := s.Store.LatestForgeEvents(r.Context(), now.AddDate(0, 0, -connectionEventWindowDays))
	if err != nil {
		operatorReadError(w, err)
		return
	}
	var boards []ConnectionBoard
	for _, b := range c.Boards {
		if b.Team == "" || principal.AllowsTeam(b.Team) {
			boards = append(boards, b)
		}
	}
	teams := s.connectionTeams(principal)
	var checkedAt time.Time
	for _, b := range boards {
		view, at := connectionBoard(c, b, teams)
		if !at.IsZero() {
			checkedAt = at
		}
		body.Boards = append(body.Boards, view)
	}
	if !checkedAt.IsZero() {
		body.CheckedAt = &checkedAt
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectionReadinessTimeout)
	defer cancel()
	for _, repo := range c.Repositories {
		body.Repositories = append(body.Repositories, connectionRepository(ctx, c, repo, boards, events))
	}
	operatorJSON(w, 200, body)
}

func (s *Server) connectionTeams(principal OperatorPrincipal) map[string][]string {
	out := map[string][]string{}
	for team, assignees := range s.OperatorConfig.TeamAssignees {
		if principal.AllowsTeam(team) && len(assignees) > 0 {
			out[team] = slices.Sorted(slices.Values(assignees))
		}
	}
	return out
}

func connectionBoard(c *Connections, b ConnectionBoard, teams map[string][]string) (connectionBoardV1, time.Time) {
	view := connectionBoardV1{Tracker: b.Tracker, Name: b.Name, Team: optional(b.Team), Repository: optional(b.Repository),
		Default: optional(b.Default), Allow: append([]string{}, b.Allow...), Webhook: BoardWebhookUnchecked, Teams: []connectionTeamV1{}}
	if b.ID != "" {
		view.ProjectID = optional(b.ID)
	}
	var wiring BoardWiring
	var at time.Time
	checked := false
	if b.Tracker == "vikunja" {
		wiring, at, checked = c.Wiring(b)
		if checked {
			view.Webhook = wiring.Webhook
			if wiring.ProjectID != "" {
				view.ProjectID = optional(wiring.ProjectID)
			}
		}
	}
	names := make([]string, 0, len(teams))
	for team := range teams {
		if b.Team == "" || b.Team == team {
			names = append(names, team)
		}
	}
	sort.Strings(names)
	for _, team := range names {
		entry := connectionTeamV1{Team: team, Assignees: []connectionAssigneeV1{}}
		for _, username := range teams[team] {
			a := connectionAssigneeV1{Username: username}
			if checked && wiring.Shared != nil {
				shared := slices.Contains(wiring.Shared, username)
				a.Shared = &shared
			}
			entry.Assignees = append(entry.Assignees, a)
		}
		view.Teams = append(view.Teams, entry)
	}
	return view, at
}

func connectionRepository(ctx context.Context, c *Connections, repo ConnectionRepository, boards []ConnectionBoard, events map[string]time.Time) connectionRepositoryV1 {
	t := repo.Target
	forge := t.Forge
	view := connectionRepositoryV1{Key: repo.Key, Label: optional(repo.Label), Forge: forge, Owner: t.Owner, Repo: t.Repo,
		BaseBranch: t.BaseBranch, Readiness: "unknown", BoardsRoutingTo: []string{}}
	if c.Readiness != nil {
		v := c.Readiness.Admit(ctx, repo.Key)
		switch {
		case v.Ready:
			view.Readiness = "ready"
		case v.Unknown:
			view.Reason = v.Reason
		default:
			view.Readiness, view.Reason = "not_ready", v.Reason
		}
	}
	if at, ok := events[store.ForgeEventKey(forge, t.Owner+"/"+t.Repo)]; ok {
		view.LastForgeEvent = &at
	}
	for _, b := range boards {
		if b.Repository == repo.Key || (repo.Label != "" && (b.Default == repo.Label || slices.Contains(b.Allow, repo.Label))) {
			view.BoardsRoutingTo = append(view.BoardsRoutingTo, b.Name)
		}
	}
	return view
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

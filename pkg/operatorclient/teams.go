package operatorclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

// TeamView is one Team as the operator API's Team routes serve it
// (ADR-0198): a static Team's read, or a runtime Team's with its manifest
// and revision.
type TeamView struct {
	store.OperatorTeam
	Revision      string         `json:"revision,omitempty"`
	Manifest      *team.Manifest `json:"manifest,omitempty"`
	ShadowedBy    string         `json:"shadowedBy,omitempty"`
	InvalidReason string         `json:"invalidReason,omitempty"`
	DrainDeadline string         `json:"drainDeadline,omitempty"`
}

// TeamDryRun is what creating or replacing a runtime Team would store: the
// manifest with its defaults filled, the state it would be in and the spend
// headroom left in the Tenant after it.
type TeamDryRun struct {
	Manifest      team.Manifest `json:"manifest"`
	State         string        `json:"state"`
	InvalidReason string        `json:"invalidReason,omitempty"`
	HeadroomUSD   float64       `json:"headroomUsd"`
}

// TeamChange is the answer to a Team write. Created is true when a create
// stored a new Team, false when it found the same one; Draining is true
// when a removal started a drain instead of removing at once.
type TeamChange struct {
	Team     *TeamView `json:"team"`
	Created  bool      `json:"created"`
	Draining bool      `json:"-"`
}

// TeamList answers GET /teams with each Team's identity and, for a runtime
// Team, its manifest and revision.
func (c *Client) TeamList(ctx context.Context) ([]TeamView, error) {
	var out struct {
		Teams []TeamView `json:"teams"`
	}
	return out.Teams, c.get(ctx, "teams", nil, &out)
}

// Team answers GET /teams/{team}; ref is an id, a key or an alias.
func (c *Client) Team(ctx context.Context, ref string) (TeamView, error) {
	var out struct {
		Team TeamView `json:"team"`
	}
	path, err := teamPath(ref)
	if err != nil {
		return out.Team, err
	}
	return out.Team, c.get(ctx, path, nil, &out)
}

// DryRunCreateTeam answers POST /teams?dryRun=true: what creating manifest
// would store, without storing it.
func (c *Client) DryRunCreateTeam(ctx context.Context, who Attribution, manifest any) (TeamDryRun, error) {
	var out TeamDryRun
	return out, c.teamWrite(ctx, http.MethodPost, "teams", dryRun(), who, "", manifest, &out)
}

// CreateTeam answers POST /teams: the same key with the same body again
// returns the existing Team with Created false.
func (c *Client) CreateTeam(ctx context.Context, who Attribution, manifest any) (TeamChange, error) {
	var out TeamChange
	return out, c.teamWrite(ctx, http.MethodPost, "teams", nil, who, "", manifest, &out)
}

// DryRunReplaceTeam answers PUT /teams/{team}?dryRun=true for revision.
func (c *Client) DryRunReplaceTeam(ctx context.Context, who Attribution, ref, revision string, manifest any) (TeamDryRun, error) {
	var out TeamDryRun
	path, err := teamPath(ref)
	if err != nil {
		return out, err
	}
	return out, c.teamWrite(ctx, http.MethodPut, path, dryRun(), who, revision, manifest, &out)
}

// ReplaceTeam answers PUT /teams/{team}: it replaces a runtime Team's
// manifest, key included, when revision is still its current one.
func (c *Client) ReplaceTeam(ctx context.Context, who Attribution, ref, revision string, manifest any) (TeamChange, error) {
	var out TeamChange
	path, err := teamPath(ref)
	if err != nil {
		return out, err
	}
	return out, c.teamWrite(ctx, http.MethodPut, path, nil, who, revision, manifest, &out)
}

// RenameTeam answers PATCH /teams/{team}: it changes only a runtime Team's
// display name, when revision is still its current one.
func (c *Client) RenameTeam(ctx context.Context, who Attribution, ref, revision, name string) (TeamChange, error) {
	var out TeamChange
	path, err := teamPath(ref)
	if err != nil {
		return out, err
	}
	return out, c.teamWrite(ctx, http.MethodPatch, path, nil, who, revision, map[string]string{"name": name}, &out)
}

// RemoveTeam answers DELETE /teams/{team}. With drain the Team is removed
// once its open Work Items finish, and the answer carries it with Draining
// true; without, it is removed at once and Team is nil.
func (c *Client) RemoveTeam(ctx context.Context, who Attribution, ref string, drain bool) (TeamChange, error) {
	var out TeamChange
	path, err := teamPath(ref)
	if err != nil {
		return out, err
	}
	var q url.Values
	if drain {
		q = url.Values{"drain": {"true"}}
	}
	if err := c.teamWrite(ctx, http.MethodDelete, path, q, who, "", nil, &out); err != nil {
		return out, err
	}
	out.Draining = out.Team != nil
	return out, nil
}

func (c *Client) teamWrite(ctx context.Context, method, path string, q url.Values, who Attribution, revision string, body, into any) error {
	if !who.valid() {
		return &Error{Status: http.StatusBadRequest, Code: "actor_required", Message: "A write needs an actor of 1 to 128 identifier characters."}
	}
	header := http.Header{}
	if revision != "" {
		header.Set("If-Match", `"`+strings.Trim(revision, `"`)+`"`)
	}
	return c.send(ctx, request{method: method, path: path, query: q, who: &who, header: header, payload: body, into: into})
}

func teamPath(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !identity.MatchString(ref) {
		return "", &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "A Team is named by its id, key or alias."}
	}
	return "teams/" + ref, nil
}

func dryRun() url.Values { return url.Values{"dryRun": {"true"}} }

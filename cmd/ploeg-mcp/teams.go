package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

func registerTeamTools(srv *mcp.Server, s *server, readOnly func(string) *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "ploeg_list_teams", Title: "List Teams", Annotations: readOnly("List Teams"),
		Description: "The Teams this Operator Consumer may read, static and runtime, with id, key, aliases, source, state, revision, Roles and spend cap. Pass team (an id, key or alias) for one Team with its manifest; pass its revision to ploeg_change_team."}, s.listTeams)
}

func registerTeamActions(srv *mcp.Server, s *server) {
	addAction(srv, s, &mcp.Tool{Name: "ploeg_create_team", Title: "Create a Team", Annotations: destructive(false),
		Description: "Create a runtime Team in this consumer's Tenant from a manifest (key, optional name and charter, plan, spend cap, intake). Ploeg dry-runs it first and the person confirms the result, spend headroom included."},
		action[createTeamIn]{commandID: func(createTeamIn) string { return "" }, describe: s.describeCreateTeam, act: s.createTeam, dryRun: true})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_change_team", Title: "Change a Team", Annotations: destructive(true),
		Description: "Replace a runtime Team's manifest, key included, or change only its display name, at the revision ploeg_list_teams showed. A manifest change is dry-run first and the person confirms the result, spend headroom included."},
		action[changeTeamIn]{commandID: func(changeTeamIn) string { return "" }, describe: s.describeChangeTeam, act: s.changeTeam, dryRun: true})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_remove_team", Title: "Remove a Team", Annotations: destructive(true),
		Description: "Remove a runtime Team at once, or with drain once its open Work Items finish. Ploeg refuses while a tracker binding, an open Work Item or another Team refers to it. Asks the person to confirm first."},
		action[removeTeamIn]{commandID: func(removeTeamIn) string { return "" }, describe: s.describeRemoveTeam, act: s.removeTeam})
}

type listTeamsIn struct {
	Team string `json:"team,omitempty" jsonschema:"an id, key or alias; only this Team, with its manifest"`
}

type teamRole struct {
	Role    string `json:"role"`
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
}

type untrustedTeam struct {
	Name     string         `json:"name,omitempty"`
	Manifest *team.Manifest `json:"manifest,omitempty" jsonschema:"a runtime Team's manifest as a person stored it, with team only"`
}

type teamRow struct {
	TeamID        string                  `json:"teamId,omitempty" jsonschema:"the immutable id every record stores"`
	Key           string                  `json:"key"`
	Aliases       []string                `json:"aliases"`
	Source        string                  `json:"source,omitempty" jsonschema:"static (Ploeg's configuration file) or runtime (made through the operator API)"`
	State         string                  `json:"state,omitempty" jsonschema:"active, shadowed or invalid"`
	Revision      string                  `json:"revision,omitempty" jsonschema:"a runtime Team's revision; ploeg_change_team needs it"`
	NameSource    *team.NameSource        `json:"nameSource,omitempty"`
	QueueDepth    int64                   `json:"queueDepth"`
	Roles         []teamRole              `json:"roles"`
	SpendCap      *store.OperatorSpendCap `json:"spendCap,omitempty"`
	ShadowedBy    string                  `json:"shadowedBy,omitempty"`
	InvalidReason string                  `json:"invalidReason,omitempty"`
	DrainDeadline string                  `json:"drainDeadline,omitempty" jsonschema:"when a draining Team is removed at the latest"`
	Untrusted     untrustedTeam           `json:"untrusted"`
}

type listTeamsOut struct {
	Summary string    `json:"summary"`
	Teams   []teamRow `json:"teams"`
}

func row(v operatorclient.TeamView, withManifest bool) teamRow {
	key := cmp.Or(v.Key, v.ID)
	r := teamRow{TeamID: v.TeamID, Key: key, Aliases: nonNil(v.Aliases), Source: v.Source, State: v.State, Revision: v.Revision,
		NameSource: v.NameSource, QueueDepth: v.QueueDepth, Roles: []teamRole{}, SpendCap: v.SpendCap, ShadowedBy: v.ShadowedBy,
		InvalidReason: v.InvalidReason, DrainDeadline: v.DrainDeadline, Untrusted: untrustedTeam{Name: clip(v.Name, 80)}}
	for _, role := range v.Roles {
		l := teamRole{Role: role.ID}
		if role.Harness != nil {
			l.Harness = role.Harness.Name
		}
		if role.Model != nil {
			l.Model = *role.Model
		}
		r.Roles = append(r.Roles, l)
	}
	if withManifest && v.Source == team.SourceRuntime {
		r.Untrusted.Manifest = v.Manifest
	}
	return r
}

func (s *server) listTeams(ctx context.Context, _ *mcp.CallToolRequest, in listTeamsIn) (*mcp.CallToolResult, listTeamsOut, error) {
	out := listTeamsOut{Teams: []teamRow{}}
	if ref := strings.TrimSpace(in.Team); ref != "" {
		v, err := s.c.Team(ctx, ref)
		if err != nil {
			return nil, out, explainTeam(err)
		}
		r := row(v, true)
		out.Teams = append(out.Teams, r)
		out.Summary = fmt.Sprintf("Team %s is a %s Team, %s, with %d Roles.", r.Key, cmp.Or(r.Source, "static"), cmp.Or(r.State, "active"), len(r.Roles))
		return nil, out, nil
	}
	views, err := s.c.TeamList(ctx)
	if err != nil {
		return nil, out, explain(err)
	}
	runtime := 0
	for _, v := range views {
		out.Teams = append(out.Teams, row(v, false))
		if v.Source == team.SourceRuntime {
			runtime++
		}
	}
	out.Summary = fmt.Sprintf("%d Teams, %d of them runtime.", len(out.Teams), runtime)
	return nil, out, nil
}

func explainTeam(err error) error {
	var apiErr *operatorclient.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" && !errors.Is(err, operatorclient.ErrUnauthorized) {
		return fmt.Errorf("Ploeg refused (%d %s): %s", apiErr.Status, apiErr.Code, apiErr.Message)
	}
	return explain(err)
}

func refused(err error) error {
	return fmt.Errorf("%v Nothing changed", strings.TrimSuffix(explainTeam(err).Error(), "."))
}

type createTeamIn struct {
	Manifest map[string]any `json:"manifest" jsonschema:"the runtime Team: key, name, charter, plan {rounds [{roles [{name, writes, cap, planner, shadow, harness, model}]}]}, spend {cap {usd, period}}, maxRunning, may, intake {admissions, targets}, sponsor. Any other field is refused"`
}

func (s *server) describeCreateTeam(ctx context.Context, who operatorclient.Attribution, in createTeamIn) (string, error) {
	if len(in.Manifest) == 0 {
		return "", errors.New("a Team needs a manifest; nothing changed")
	}
	d, err := s.writer(ctx).DryRunCreateTeam(ctx, who, in.Manifest)
	if err != nil {
		return "", refused(err)
	}
	return fmt.Sprintf("Create runtime Team %s in this consumer's Tenant: %s. %s", d.Manifest.Key, manifestEffect(d.Manifest), dryRunEffect(d)), nil
}

func (s *server) createTeam(ctx context.Context, who operatorclient.Attribution, _ string, in createTeamIn) (actionOut, error) {
	c, err := s.writer(ctx).CreateTeam(ctx, who, in.Manifest)
	if err != nil {
		return actionOut{}, err
	}
	out := teamOut(c.Team)
	out.Summary = fmt.Sprintf("Created runtime Team %s (%s) at revision %s; it is %s.", out.Team, out.TeamID, out.Revision, out.State)
	if !c.Created {
		out.Replayed = true
		out.Summary = fmt.Sprintf("Runtime Team %s already exists with this manifest; nothing changed now.", out.Team)
	}
	return out, nil
}

type changeTeamIn struct {
	Team     string         `json:"team" jsonschema:"the runtime Team's id, key or alias"`
	Revision string         `json:"revision" jsonschema:"the Team's revision from ploeg_list_teams; a change to an older revision is refused"`
	Manifest map[string]any `json:"manifest,omitempty" jsonschema:"the whole new manifest, as ploeg_create_team takes it; a new key renames the Team and keeps the old key as an alias"`
	Name     *string        `json:"name,omitempty" jsonschema:"only a new display name, 1 to 80 characters; give either manifest or name"`
}

func (s *server) describeChangeTeam(ctx context.Context, who operatorclient.Attribution, in changeTeamIn) (string, error) {
	if (len(in.Manifest) > 0) == (in.Name != nil) {
		return "", errors.New("give either a whole manifest or only a name; nothing changed")
	}
	if strings.TrimSpace(in.Revision) == "" {
		return "", errors.New("a change needs the Team's revision from ploeg_list_teams; nothing changed")
	}
	cur, err := s.c.Team(ctx, in.Team)
	if err != nil {
		return "", refused(err)
	}
	if in.Name != nil && cur.Source == team.SourceStatic {
		return "", staticName(cur)
	}
	if cur.Source == team.SourceRuntime && strings.Trim(in.Revision, `"`) != cur.Revision {
		return "", fmt.Errorf("Ploeg refused (412 stale_revision): Team %s is at revision %s, not %s; read it again with ploeg_list_teams. Nothing changed", cur.Key, cur.Revision, in.Revision)
	}
	if in.Name != nil {
		return fmt.Sprintf("Change the display name of runtime Team %s (revision %s) from %q to %q. Nothing else changes and nothing is spent.",
			cur.Key, in.Revision, clip(cur.Name, 80), clip(*in.Name, 80)), nil
	}
	d, err := s.writer(ctx).DryRunReplaceTeam(ctx, who, in.Team, in.Revision, in.Manifest)
	if err != nil {
		return "", refused(err)
	}
	effect := fmt.Sprintf("Replace runtime Team %s (revision %s) with: %s.", cur.Key, in.Revision, manifestEffect(d.Manifest))
	if d.Manifest.Key != cur.Key {
		effect += fmt.Sprintf(" This renames it to %s; %s stays an alias of the same Team.", d.Manifest.Key, cur.Key)
	}
	return effect + " " + dryRunEffect(d), nil
}

func staticName(cur operatorclient.TeamView) error {
	key := team.ConfigurationNameKey(cur.Key)
	if cur.NameSource != nil && cur.NameSource.Key != "" {
		key = cur.NameSource.Key
	}
	return fmt.Errorf("Ploeg refused (409 name_set_by_configuration): Team %s is configured in Ploeg's configuration file, which alone sets its name. Change %s in that file and redeploy. Nothing changed", cur.Key, key)
}

func (s *server) changeTeam(ctx context.Context, who operatorclient.Attribution, _ string, in changeTeamIn) (actionOut, error) {
	w := s.writer(ctx)
	var c operatorclient.TeamChange
	var err error
	if in.Name != nil {
		c, err = w.RenameTeam(ctx, who, in.Team, in.Revision, *in.Name)
	} else {
		c, err = w.ReplaceTeam(ctx, who, in.Team, in.Revision, in.Manifest)
	}
	if err != nil {
		return actionOut{}, err
	}
	out := teamOut(c.Team)
	out.Summary = fmt.Sprintf("Changed runtime Team %s; it is at revision %s and %s.", out.Team, out.Revision, out.State)
	return out, nil
}

type removeTeamIn struct {
	Team  string `json:"team" jsonschema:"the runtime Team's id, key or alias"`
	Drain bool   `json:"drain,omitempty" jsonschema:"refuse new admissions, let open Work Items finish (at most 14 days), then remove it"`
}

func (s *server) describeRemoveTeam(ctx context.Context, _ operatorclient.Attribution, in removeTeamIn) (string, error) {
	cur, err := s.c.Team(ctx, in.Team)
	if err != nil {
		return "", refused(err)
	}
	if cur.Source == team.SourceStatic {
		return "", fmt.Errorf("Ploeg refused (409 team_is_static): Team %s is configured in Ploeg's configuration file; remove it there. Nothing changed", cur.Key)
	}
	named := fmt.Sprintf("runtime Team %s (%s, %s)", cur.Key, cur.TeamID, cmp.Or(cur.State, "active"))
	if in.Drain {
		return fmt.Sprintf("Drain %s: Ploeg refuses new admissions for it, lets its open Work Items finish and then removes it. After 14 days its remaining open Work Items move to needs_human and it is removed. Its key and aliases stay reserved for 90 days.", named), nil
	}
	return fmt.Sprintf("Remove %s now. Ploeg refuses while a tracker binding, an open Work Item or another Team's refinementTeam refers to it. Its key and aliases stay reserved for 90 days; money already spent stays spent.", named), nil
}

func (s *server) removeTeam(ctx context.Context, who operatorclient.Attribution, _ string, in removeTeamIn) (actionOut, error) {
	c, err := s.writer(ctx).RemoveTeam(ctx, who, in.Team, in.Drain)
	if err != nil {
		return actionOut{}, err
	}
	if c.Draining {
		out := teamOut(c.Team)
		out.Summary = fmt.Sprintf("Runtime Team %s is draining; Ploeg removes it once its open Work Items finish, by %s at the latest.", out.Team, c.Team.DrainDeadline)
		return out, nil
	}
	return actionOut{Team: strings.TrimSpace(in.Team), State: "removed", Summary: fmt.Sprintf("Removed runtime Team %s.", strings.TrimSpace(in.Team))}, nil
}

func teamOut(v *operatorclient.TeamView) actionOut {
	if v == nil {
		return actionOut{}
	}
	return actionOut{Team: cmp.Or(v.Key, v.ID), TeamID: v.TeamID, Revision: v.Revision, State: v.State}
}

func manifestEffect(m team.Manifest) string {
	var rounds []string
	for i, r := range m.Plan.Rounds {
		var roles []string
		for _, role := range r.Roles {
			l := fmt.Sprintf("%s on %s with %s", role.Name, role.Harness, role.Model)
			var marks []string
			if role.Writes {
				marks = append(marks, "writes")
			}
			if role.Cap > 0 {
				marks = append(marks, "capped at "+usd(role.Cap))
			}
			if role.Shadow {
				marks = append(marks, "shadow")
			}
			if len(marks) > 0 {
				l += " (" + strings.Join(marks, ", ") + ")"
			}
			roles = append(roles, l)
		}
		rounds = append(rounds, fmt.Sprintf("Round %d %s", i+1, strings.Join(roles, " and ")))
	}
	effect := fmt.Sprintf("named %q, %s", clip(m.Name, 80), strings.Join(rounds, "; "))
	if c := m.Spend.Cap; c != nil {
		effect += fmt.Sprintf(", spending at most %s per %s", usd(c.USD), c.Period)
	}
	if m.Intake.Admissions {
		effect += ", taking operator admissions"
	}
	return effect
}

func dryRunEffect(d operatorclient.TeamDryRun) string {
	state := d.State
	if d.InvalidReason != "" {
		state += " (" + d.InvalidReason + ")"
	}
	return fmt.Sprintf("Ploeg's dry run: it would be %s, leaving %s of the Tenant's runtime Team allowance. Its Runs may then spend up to its cap on models.", state, usd(d.HeadroomUSD))
}

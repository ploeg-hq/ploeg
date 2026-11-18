package toolregistry

import (
	"context"
	"errors"
)

// TeamToolsView is a Team's attachment and the grant each Role gets.
type TeamToolsView struct {
	Team       Team
	Attachment Attachment
	Exists     bool
	Grants     map[string]Grant
}

func (s *Service) team(ref string) (Team, *Error) {
	if s.Teams == nil {
		return Team{}, refuse(404, "unknown_team", "No Team has this id, key or alias.")
	}
	t, ok := s.Teams.Resolve(ref)
	if !ok {
		return Team{}, refuse(404, "unknown_team", "No Team has this id, key or alias.")
	}
	return t, nil
}

func (s *Service) attachment(ctx context.Context, t Team) (Attachment, bool, *Error) {
	if roles, ok := s.StaticTools[t.Key]; ok {
		return Attachment{Tenant: s.tenant(), Team: t.Key, TeamID: t.ID, Roles: roles, Source: FromStatic}, true, nil
	}
	a, err := s.Store.TeamTools(ctx, s.tenant(), t)
	if errors.Is(err, ErrNotFound) {
		return Attachment{Tenant: s.tenant(), Team: t.Key, TeamID: t.ID, Roles: map[string]RoleTools{}}, false, nil
	}
	if err != nil {
		return Attachment{}, false, storeFault(err)
	}
	return a, true, nil
}

// TeamTools is the Team's attachment with every Role's grant.
func (s *Service) TeamTools(ctx context.Context, ref string) (TeamToolsView, *Error) {
	t, e := s.team(ref)
	if e != nil {
		return TeamToolsView{}, e
	}
	a, exists, e := s.attachment(ctx, t)
	if e != nil {
		return TeamToolsView{}, e
	}
	servers, e := s.Servers(ctx)
	if e != nil {
		return TeamToolsView{}, e
	}
	sources, e := s.Sources(ctx)
	if e != nil {
		return TeamToolsView{}, e
	}
	view := TeamToolsView{Team: t, Attachment: a, Exists: exists, Grants: map[string]Grant{}}
	for _, role := range t.Roles {
		view.Grants[role.Name] = BuildGrant(a, role, servers, sources)
	}
	return view, nil
}

// ReplaceTeamTools validates and stores a Team's attachment. revision nil
// creates (If-None-Match: *); dryRun validates and writes nothing.
func (s *Service) ReplaceTeamTools(ctx context.Context, audit Audit, ref string, roles map[string]RoleTools, revision *int64, dryRun bool) (TeamToolsView, *Error) {
	if e := s.refuseRuntime(); e != nil {
		return TeamToolsView{}, e
	}
	t, e := s.team(ref)
	if e != nil {
		return TeamToolsView{}, e
	}
	if _, static := s.StaticTools[t.Key]; static || t.ToolsInFile {
		return TeamToolsView{}, refuseField(409, "attachment_set_by_configuration", "teams."+t.Key+".tools", "This Team's tools are set in the configuration file at teams."+t.Key+".tools; change them there.")
	}
	servers, e := s.Servers(ctx)
	if e != nil {
		return TeamToolsView{}, e
	}
	sources, e := s.Sources(ctx)
	if e != nil {
		return TeamToolsView{}, e
	}
	resolved, e := CheckAttachment(t, roles, servers, sources)
	if e != nil {
		return TeamToolsView{}, e
	}
	a := Attachment{Tenant: s.tenant(), Team: t.Key, TeamID: t.ID, Roles: resolved, Source: FromRuntime}
	if !dryRun {
		stored, err := s.Store.ReplaceTeamTools(ctx, s.tenant(), t, resolved, revision, audit)
		if err != nil {
			return TeamToolsView{}, s.mapStoreError(err, "attachment")
		}
		a = stored
	} else if current, exists, e := s.attachment(ctx, t); e == nil && exists {
		a.Revision = current.Revision
	}
	view := TeamToolsView{Team: t, Attachment: a, Exists: true, Grants: map[string]Grant{}}
	for _, role := range t.Roles {
		view.Grants[role.Name] = BuildGrant(a, role, servers, sources)
	}
	return view, nil
}

// RemoveTeamTools removes a Team's runtime attachment.
func (s *Service) RemoveTeamTools(ctx context.Context, audit Audit, ref string) *Error {
	if e := s.refuseRuntime(); e != nil {
		return e
	}
	t, e := s.team(ref)
	if e != nil {
		return e
	}
	if _, static := s.StaticTools[t.Key]; static || t.ToolsInFile {
		return refuseField(409, "attachment_set_by_configuration", "teams."+t.Key+".tools", "This Team's tools are set in the configuration file at teams."+t.Key+".tools; change them there.")
	}
	if err := s.Store.RemoveTeamTools(ctx, s.tenant(), t, audit); err != nil {
		return storeFault(err)
	}
	return nil
}

// GrantFor is the grant of a Run of tenant, team and role, the part of the
// credential gateway's grant that names servers and skills. A Run of
// another Tenant, or one that names none, gets nothing from this Tenant's
// registry.
func (s *Service) GrantFor(ctx context.Context, tenant, team, role string) (Grant, *Error) {
	if tenant != s.tenant() {
		return Grant{}, refuse(403, "tenant_mismatch", "The Run's Tenant is not the Tenant this registry serves.")
	}
	t, e := s.team(team)
	if e != nil {
		return Grant{}, e
	}
	r, ok := t.Role(role)
	if !ok {
		return Grant{Servers: []ServerGrant{}, Skills: []SkillGrant{}}, nil
	}
	a, exists, e := s.attachment(ctx, t)
	if e != nil || !exists {
		return Grant{Servers: []ServerGrant{}, Skills: []SkillGrant{}}, e
	}
	servers, e := s.Servers(ctx)
	if e != nil {
		return Grant{}, e
	}
	sources, e := s.Sources(ctx)
	if e != nil {
		return Grant{}, e
	}
	return BuildGrant(a, r, servers, sources), nil
}

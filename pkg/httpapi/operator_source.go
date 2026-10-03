package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func sourceAPIURL(value string) (string, bool) {
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Path, "\\%") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return "", false
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), true
}

func (s *Server) freshOperatorSource(ctx context.Context, source store.OperatorSource) (work.WorkItem, error) {
	t, ok := s.Trackers[source.Provider]
	if !ok {
		return work.WorkItem{}, store.ErrExecutionConflict
	}
	reader, ok := t.(provider.ExecutionReader)
	wanted, valid := sourceAPIURL(source.ExpectedBaseURL)
	if !ok || !valid {
		return work.WorkItem{}, store.ErrExecutionConflict
	}
	configured, valid := sourceAPIURL(reader.TrackerAPIBaseURL())
	if !valid || configured != wanted {
		return work.WorkItem{}, store.ErrExecutionConflict
	}
	fresh, err := reader.FetchExecutionItem(ctx, source.ExternalID)
	if err != nil || !fresh.Open || fresh.Item.Provider != source.Provider || fresh.Item.ExternalID != source.ExternalID || fresh.Item.ExternalScope != source.ExpectedScope || fresh.Item.Revision == "" || fresh.Item.Revision != source.ExpectedRevision {
		return work.WorkItem{}, store.ErrExecutionConflict
	}
	return fresh.Item, nil
}

func (s *Server) currentOperatorSource(ctx context.Context, source store.OperatorSource, team string) error {
	fresh, err := s.freshOperatorSource(ctx, source)
	if err != nil {
		return err
	}
	if !s.operatorSourceTargetMatches(source, team, fresh.Labels) {
		return store.ErrExecutionConflict
	}
	return nil
}

func (s *Server) operatorSourceTargetMatches(source store.OperatorSource, team string, labels []string) bool {
	if s.Targets == nil || (s.ScopeTeams[source.ExpectedScope] != "" && s.ScopeTeams[source.ExpectedScope] != team) {
		return false
	}
	route, ok, err := s.Targets.Route(target.Request{Scope: source.ExpectedScope, Team: team, Labels: labels, LabelsRead: true})
	if err != nil || !ok {
		return false
	}
	resolved := route.Target
	return source.ExpectedTarget == (store.OperatorTarget{Forge: resolved.Forge, Owner: resolved.Owner, Repo: resolved.Repo, BaseBranch: resolved.BaseBranch})
}

func (s *Server) handleOperatorSourceLookup(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "provider" && key != "externalId" && key != "scope" && key != "baseUrl") || len(values) != 1 {
			operatorError(w, 400, "invalid_source", "Supply one configured tracker source.")
			return
		}
	}
	if !operatorName.MatchString(query.Get("provider")) || !operatorName.MatchString(query.Get("externalId")) || !operatorName.MatchString(query.Get("scope")) {
		operatorError(w, 400, "invalid_source", "Supply valid tracker identity and scope.")
		return
	}
	base, valid := sourceAPIURL(query.Get("baseUrl"))
	if !valid {
		operatorError(w, 400, "invalid_source", "Supply the configured tracker API root.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	item, scope, err := s.Store.OperatorSourceLookup(r.Context(), query.Get("provider"), query.Get("externalId"), principal.Teams)
	if err != nil {
		if errors.Is(err, store.ErrOperatorNotFound) {
			operatorError(w, 404, "not_found", "Work Item not found.")
		} else {
			executionError(w, err)
		}
		return
	}
	source := store.OperatorSource{WorkItemID: item.ID, Provider: item.Provider, ExternalID: item.ExternalID, ExpectedScope: scope, ExpectedBaseURL: base, ExpectedRevision: item.Revision, ExpectedUpdatedAt: item.UpdatedAt, ExpectedTarget: *item.Target}
	if scope != query.Get("scope") || s.currentOperatorSource(r.Context(), source, item.Team) != nil {
		operatorError(w, 409, "source_changed", "The tracker source is unavailable, changed, closed or outside the configured scope.")
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "item": item, "source": source})
}

func validOperatorSource(source store.OperatorSource) bool {
	_, err := store.OperatorCursor(source.WorkItemID)
	_, valid := sourceAPIURL(source.ExpectedBaseURL)
	return err == nil && source.WorkItemID != "0" && operatorName.MatchString(source.Provider) && operatorName.MatchString(source.ExternalID) && operatorName.MatchString(source.ExpectedScope) && source.ExpectedRevision != "" && len(source.ExpectedRevision) <= 512 && !source.ExpectedUpdatedAt.IsZero() && valid && source.ExpectedTarget.Forge != "" && source.ExpectedTarget.Owner != "" && source.ExpectedTarget.Repo != "" && source.ExpectedTarget.BaseBranch != ""
}

func (s *Server) validateOperatorSource(ctx context.Context, input store.AdmitOperatorExecution) error {
	source := input.Source
	if !validOperatorSource(*source) || input.BaseBranch != source.ExpectedTarget.BaseBranch {
		return store.ErrExecutionConflict
	}
	if err := s.currentOperatorSource(ctx, *source, input.Team); err != nil {
		return err
	}
	forge, ok := s.Forges[source.ExpectedTarget.Forge].(provider.ForgeRepositoryLocator)
	if !ok {
		return store.ErrExecutionConflict
	}
	expected, err := forge.RepositoryURL(source.ExpectedTarget.Owner, source.ExpectedTarget.Repo)
	if err != nil || strings.TrimSuffix(input.RepositoryURL, ".git") != strings.TrimSuffix(expected, ".git") {
		return store.ErrExecutionConflict
	}
	return nil
}

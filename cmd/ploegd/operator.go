package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/plan"
)

func operatorConfig(cfg *config.File, plans plan.Plans) (httpapi.OperatorConfig, error) {
	consumers, err := httpapi.ParseOperatorConsumers(os.Getenv("PLOEG_OPERATOR_CONSUMERS"), os.LookupEnv)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	personTokens, err := personTokens(consumers)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	attribution, err := attributionMode(consumers, personTokens)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	deliveryPolicies, err := httpapi.ParseDeliveryPolicies(os.Getenv("PLOEG_OPERATOR_DELIVERY_POLICIES"))
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	harnesses, err := httpapi.ParseTeamHarnesses(os.Getenv("PLOEG_TEAM_HARNESSES"))
	if err != nil {
		return httpapi.OperatorConfig{}, fmt.Errorf("PLOEG_TEAM_HARNESSES: %w", err)
	}
	teams := map[string][]string{}
	for name := range cfg.Teams {
		teams[name] = []string{}
	}
	assignees := map[string][]string{}
	for username, name := range mergeTeamMap(cfg.AssigneeTeams(), parseTeamMap(os.Getenv("PLOEG_TEAM_MAP"))) {
		if _, exists := teams[name]; !exists {
			teams[name] = []string{}
		}
		assignees[name] = append(assignees[name], username)
	}
	for name := range assignees {
		sort.Strings(assignees[name])
	}
	scopes := map[string][]string{}
	for scope, name := range cfg.ScopeTeams() {
		if _, exists := teams[name]; !exists {
			teams[name] = []string{}
		}
		scopes[name] = append(scopes[name], scope)
	}
	for name := range scopes {
		sort.Strings(scopes[name])
	}
	for name, p := range plans {
		roles := map[string]bool{}
		for _, round := range p.Rounds {
			for _, role := range round.Roles {
				roles[role.Name] = true
			}
		}
		teams[name] = []string{}
		for role := range roles {
			teams[name] = append(teams[name], role)
		}
		sort.Strings(teams[name])
	}
	if name := os.Getenv("PLOEG_DEFAULT_TEAM"); name != "" {
		if _, exists := teams[name]; !exists {
			teams[name] = []string{}
		}
	}
	return httpapi.OperatorConfig{Consumers: consumers, PersonTokens: personTokens, Teams: teams, TeamAssignees: assignees, TeamScopes: scopes,
		DeliveryPolicies: deliveryPolicies, TeamHarnesses: harnesses, Attribution: attribution}, nil
}

func attributionMode(consumers []httpapi.OperatorConsumer, personTokens *httpapi.PersonTokens) (httpapi.AttributionMode, error) {
	mode, err := httpapi.ParseAttributionMode(os.Getenv("PLOEG_OPERATOR_ATTRIBUTION"))
	if err != nil || len(consumers) == 0 {
		return mode, err
	}
	if mode == httpapi.AttributionProven && personTokens == nil {
		return "", errors.New("PLOEG_OPERATOR_ATTRIBUTION=proven accepts an operator change only with a person token, which needs PLOEG_OPERATOR_PERSON_ISSUER and a consumer naming audiences or clientIds")
	}
	if mode == httpapi.AttributionAsserted {
		slog.Warn("operator changes may name their person in X-Ploeg-Actor alone, so a consumer's token can act as anyone in its Teams; set PLOEG_OPERATOR_ATTRIBUTION=proven once every consumer sends person tokens (the chart defaults to proven from 0.3.0, threat model F-07)")
	}
	return mode, nil
}

func personTokens(consumers []httpapi.OperatorConsumer) (*httpapi.PersonTokens, error) {
	issuer := strings.TrimSpace(os.Getenv("PLOEG_OPERATOR_PERSON_ISSUER"))
	if issuer == "" {
		if httpapi.OperatorConsumersNameClientIDs(consumers) {
			return nil, errors.New("PLOEG_OPERATOR_CONSUMERS names clientIds or audiences, which need PLOEG_OPERATOR_PERSON_ISSUER")
		}
		return nil, nil
	}
	cfg := httpapi.PersonTokenConfig{
		Issuer:           issuer,
		Audience:         strings.TrimSpace(os.Getenv("PLOEG_OPERATOR_PERSON_AUDIENCE")),
		JWKSURL:          strings.TrimSpace(os.Getenv("PLOEG_OPERATOR_PERSON_JWKS_URL")),
		SubjectNamespace: strings.TrimSpace(os.Getenv("PLOEG_OPERATOR_PERSON_SUBJECT_NAMESPACE")),
	}
	if raw := strings.TrimSpace(os.Getenv("PLOEG_OPERATOR_PERSON_MAX_LIFETIME")); raw != "" {
		lifetime, err := time.ParseDuration(raw)
		if err != nil || lifetime <= 0 {
			return nil, errors.New("PLOEG_OPERATOR_PERSON_MAX_LIFETIME must be a positive duration such as 5m")
		}
		cfg.MaxLifetime = lifetime
	}
	tokens, err := httpapi.NewPersonTokens(cfg, consumers)
	if err != nil {
		return nil, fmt.Errorf("PLOEG_OPERATOR_PERSON_*: %w", err)
	}
	if open := httpapi.OperatorConsumersActingForAnyPerson(consumers); len(open) > 0 {
		slog.Warn("operator consumers with execute accept a person token as the bearer and require no claim of it, so the identity provider alone decides who acts with their authority; set requireClaims (profile P1 refuses this from chart 0.3.0, threat model F-47)",
			"consumers", strings.Join(open, ","))
	}
	return tokens, nil
}

func deployAuth() (*httpapi.DeployAuth, error) {
	auth, err := httpapi.NewDeployAuth(os.Getenv("PLOEG_DEPLOY_TOKEN"))
	if err != nil {
		return nil, fmt.Errorf("PLOEG_DEPLOY_TOKEN: %w", err)
	}
	return auth, nil
}

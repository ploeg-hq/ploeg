package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/rarity"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func operatorConfig(cfg *config.File, plans plan.Plans) (httpapi.OperatorConfig, error) {
	consumers, err := httpapi.ParseOperatorConsumers(os.Getenv("PLOEG_OPERATOR_CONSUMERS"), os.LookupEnv)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	deliveryPolicies, err := httpapi.ParseDeliveryPolicies(os.Getenv("PLOEG_OPERATOR_DELIVERY_POLICIES"))
	if err != nil {
		return httpapi.OperatorConfig{}, err
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
	styles, err := cardStyles(cfg)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	releases, err := cfg.ReleaseEnvironments()
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	matchers, err := rarityMatchers(cfg)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	shapes, err := shapeMatchers(cfg)
	if err != nil {
		return httpapi.OperatorConfig{}, err
	}
	return httpapi.OperatorConfig{Consumers: consumers, Teams: teams, TeamAssignees: assignees, TeamScopes: scopes,
		DeliveryPolicies: deliveryPolicies, CardStyles: styles, ReleaseEnvironments: releases, RarityMatchers: matchers,
		ShapeMatchers: shapes}, nil
}

func shapeMatchers(cfg *config.File) (map[string]playkpi.Matcher, error) {
	rules, err := cfg.CardShapeRules()
	if err != nil {
		return nil, err
	}
	out := make(map[string]playkpi.Matcher, len(rules))
	for repo, r := range rules {
		m, err := r.Compile()
		if err != nil {
			return nil, fmt.Errorf("cardShape rules of %s: %w", repo, err)
		}
		out[repo] = m
	}
	return out, nil
}

func rarityMatchers(cfg *config.File) (map[string]rarity.Matcher, error) {
	rules, err := cfg.RarityRules()
	if err != nil {
		return nil, err
	}
	out := make(map[string]rarity.Matcher, len(rules))
	for repo, r := range rules {
		m, err := r.Compile()
		if err != nil {
			return nil, fmt.Errorf("rarity rules of %s: %w", repo, err)
		}
		out[repo] = m
	}
	return out, nil
}

func deployAuth() (*httpapi.DeployAuth, error) {
	auth, err := httpapi.NewDeployAuth(os.Getenv("PLOEG_DEPLOY_TOKEN"))
	if err != nil {
		return nil, fmt.Errorf("PLOEG_DEPLOY_TOKEN: %w", err)
	}
	return auth, nil
}

func cardStyles(cfg *config.File) (map[string]store.CardStyle, error) {
	configured, err := cfg.CardStyles()
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.CardStyle, len(configured))
	for repo, style := range configured {
		card := store.CardStyle{Skin: style.Skin}
		if style.Theme != "" {
			theme := style.Theme
			card.Theme = &theme
		}
		out[repo] = card
	}
	return out, nil
}

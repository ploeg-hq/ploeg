package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

type targetConnections struct{ svc *connection.Service }

func (c targetConnections) ForgeConnection(ctx context.Context, id string) (worktarget.ConnectionInfo, bool, error) {
	info, found, err := c.svc.ForgeConnection(ctx, id)
	return worktarget.ConnectionInfo{ID: info.ID, Kind: info.Kind, Instance: info.Instance, State: info.State, Runtime: info.Runtime, CanMint: info.CanMint}, found, err
}

func (c targetConnections) WebhookSecret(ctx context.Context, id string) (string, bool, error) {
	return c.svc.WebhookSecret(ctx, id)
}

func (c targetConnections) Credential(ctx context.Context, id string) (provider.ConnectionCredential, error) {
	return c.svc.Credential(ctx, id)
}

func (c targetConnections) ConnectionsOfInstallation(ctx context.Context, appID, installationID string) ([]string, error) {
	return c.svc.ConnectionsOfInstallation(ctx, appID, installationID)
}

// runtimeTargets builds the runtime Work Target service of ADR-0193 when
// the configuration file opts in and runtime connections exist, joins it to
// the readiness gate and the binding registry, and loads it once. Nil
// registers no runtime target.
func runtimeTargets(ctx context.Context, log *slog.Logger, cfg *config.File, st *store.Store, conns *connection.Service,
	static target.Table, gate *target.Gate, bindings *trackerbinding.Registry, forgeID string, directory *httpapi.ForgeDirectory) *worktarget.Service {
	if cfg.TargetRegistration == nil {
		return nil
	}
	if conns == nil {
		log.Warn("targetRegistration is set but no connections section is; no runtime Work Target can be registered")
		return nil
	}
	svc := &worktarget.Service{Store: st, Config: cfg.TargetRegistration, Connections: targetConnections{svc: conns},
		Connectors: conns.Connectors, Instances: conns.Instances, Static: static.Targets, DefaultForge: forgeID,
		Gate: gate, Bindings: bindings, Log: log, ForgeHosts: forgeHosts(directory), Minting: map[string]bool{"github": true},
		PublicWebhookBase: conns.PublicWebhookBase}
	bindings.Runtime = svc
	bindingUsers := conns.Users
	conns.Users = func(ctx context.Context, id string) ([]connection.Use, error) {
		var out []connection.Use
		if bindingUsers != nil {
			uses, err := bindingUsers(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, uses...)
		}
		targets, err := svc.Users(ctx, id)
		for _, u := range targets {
			out = append(out, connection.Use{Kind: "work_target", ID: u.Key, State: u.State})
		}
		return out, err
	}
	if err := svc.Rebuild(ctx); err != nil {
		log.Warn("runtime Work Targets not loaded; the static targets alone are in force until the next poll", "err", err)
	}
	log.Info("runtime Work Targets configured", "enabled", cfg.TargetRegistration.Enabled, "live", len(svc.Live()))
	return svc
}

// connectionBroker mints a Run's credential through the runtime forge
// connection of its Work Target (ADR-0193 section 4, ADR-0192): a GitHub App
// installation's token pinned to that installation, or a GitLab project
// access token minted with a service account's token (ADR-0203). A GitLab
// OAuth connection acts as a person and mints nothing; other kinds mint no
// per-Run credential yet. Both refuse. projects names the repository ids of
// a connection's live targets, which the GitLab sweep lists.
func connectionBroker(conns *connection.Service, projects func(connection string) []string) forgebroker.ConnectionResolver {
	return func(ctx context.Context, id string) (forgebroker.Broker, error) {
		info, found, err := conns.ForgeConnection(ctx, id)
		if err != nil {
			return nil, err
		}
		if found && info.Runtime && info.Kind == "gitlab" {
			return gitlabConnectionBroker(ctx, conns, info, projects)
		}
		if !found || !info.Runtime || info.Kind != "github" {
			return nil, fmt.Errorf("%w: %s", forgebroker.ErrConnectionCannotMint, id)
		}
		cred, err := conns.Credential(ctx, id)
		if err != nil {
			return nil, err
		}
		if cred.AppID == "" || cred.PrivateKey == "" || cred.InstallationID == "" {
			return nil, fmt.Errorf("%w: %s holds no App installation", forgebroker.ErrConnectionCannotMint, id)
		}
		key, err := githubapp.ParsePrivateKey([]byte(cred.PrivateKey))
		if err != nil {
			return nil, err
		}
		installation, err := strconv.ParseInt(cred.InstallationID, 10, 64)
		if err != nil {
			return nil, err
		}
		inst := conns.Instances[info.Instance]
		return &forgebroker.GitHubApp{App: &githubapp.Minter{AppID: cred.AppID, Key: key, APIURL: github.ConnectionAPIURL(inst), Installation: installation}}, nil
	}
}

func gitlabConnectionBroker(ctx context.Context, conns *connection.Service, info connection.ForgeConnectionInfo, projects func(string) []string) (forgebroker.Broker, error) {
	if info.Method != connection.MethodGitLabServiceAccount && info.Method != connection.MethodToken {
		return nil, fmt.Errorf("%w: %s is a GitLab %s connection, which acts as a person; connect GitLab with gitlab_service_account to mint per Run", forgebroker.ErrConnectionCannotMint, info.ID, info.Method)
	}
	cred, err := conns.Credential(ctx, info.ID)
	if err != nil {
		return nil, err
	}
	if cred.Token == "" {
		return nil, fmt.Errorf("%w: %s holds no token", forgebroker.ErrConnectionCannotMint, info.ID)
	}
	id := info.ID
	return &forgebroker.GitLab{APIURL: gitlab.ConnectionAPIURL(conns.Instances[info.Instance]), Token: cred.Token,
		Projects: func(context.Context) ([]string, error) {
			if projects == nil {
				return nil, nil
			}
			return projects(id), nil
		}}, nil
}

// targetProjects names the repository ids of a connection's live runtime
// targets.
func targetProjects(svc *worktarget.Service) func(string) []string {
	return func(connection string) []string {
		if svc == nil {
			return nil
		}
		var out []string
		for _, t := range svc.Live() {
			if t.Connection == connection && t.RepositoryID != "" {
				out = append(out, t.RepositoryID)
			}
		}
		return out
	}
}

// registeredRepositories names the key a repository is registered under,
// static first, then at runtime.
func registeredRepositories(static func(forge, repo string) (string, bool), svc *worktarget.Service) func(forge, repo string) (string, bool) {
	if svc == nil {
		return static
	}
	return func(forge, repo string) (string, bool) {
		if key, ok := static(forge, repo); ok {
			return key, true
		}
		return svc.Registered(forge, repo)
	}
}

func ensureGate(gate *target.Gate, cfg *config.File, forges map[string]provider.ForgeProvider) *target.Gate {
	if gate != nil || cfg.TargetRegistration == nil {
		return gate
	}
	return target.NewGate(map[string]work.Target{}, repositoryInspectors(forges))
}

// forgeHosts maps each forge id of the release to its host, so a runtime
// target on another id of the same forge meets its static targets.
func forgeHosts(directory *httpapi.ForgeDirectory) map[string]string {
	out := map[string]string{}
	if directory == nil {
		return out
	}
	for id, f := range directory.Forges {
		raw := f.PloegdURL
		if raw == "" {
			raw = f.URL
		}
		if host := worktarget.HostOf(raw); host != "" {
			out[id] = host
		}
	}
	return out
}

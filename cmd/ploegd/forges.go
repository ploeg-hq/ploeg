package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// activeForge is one forge provider ploegd serves, under its dialect.
type activeForge struct {
	provider provider.ForgeProvider
	// url is ploegd's own URL for the forge; workerURL is the one workers
	// clone from, push to and call its API at (PLOEG_<DIALECT>_WORKER_URL).
	url, workerURL string
}

// releaseForges is every forge one release serves and how each mints
// per-Run credentials (ADR-0192).
type releaseForges struct {
	// registry is the forge registry: providers by forge id (ADR-0016).
	registry     map[string]provider.ForgeProvider
	defaultForge string
	byDialect    map[string]activeForge
	// forgejoCreds is the Forgejo broker, or Static when Forgejo mints
	// nothing; githubApp is nil when no GitHub App is configured.
	forgejoCreds   forgebroker.Broker
	forgejoSweeper forgebroker.Sweeper
	githubApp      *githubapp.Minter
	// gitlabCreds mints a project access token per Run when
	// PLOEG_GITLAB_MINT_PER_RUN is on (ADR-0203); nil mints nothing.
	gitlabCreds *forgebroker.GitLab
	// runtime are the forge instances served only through runtime forge
	// connections, by instance id (runtimeForges).
	runtime map[string]httpapi.ClaimForge
}

// gitlabMinter is the static GitLab forge's per-Run broker: on with
// PLOEG_GITLAB_MINT_PER_RUN, when PLOEG_GITLAB_TOKEN is a group service
// account's token with api scope and Maintainer on the target projects.
func gitlabMinter(log *slog.Logger, active map[string]activeForge) (*forgebroker.GitLab, error) {
	on, err := switchEnv("PLOEG_GITLAB_MINT_PER_RUN", false)
	if err != nil || !on {
		return nil, err
	}
	gl, ok := active["gitlab"]
	if !ok {
		return nil, errors.New("PLOEG_GITLAB_MINT_PER_RUN is on but PLOEG_GITLAB_URL is unset")
	}
	token := os.Getenv("PLOEG_GITLAB_TOKEN")
	if token == "" {
		return nil, errors.New("PLOEG_GITLAB_MINT_PER_RUN is on but PLOEG_GITLAB_TOKEN is unset: it must be a group service account's token with api scope")
	}
	log.Info("per-run GitLab project access tokens enabled", "url", gl.url)
	return &forgebroker.GitLab{APIURL: gl.url + "/api/v4", Token: token}, nil
}

// sweepGitLabTargets lets the static GitLab broker's sweep list the projects
// of the static targets on a GitLab forge.
func (r releaseForges) sweepGitLabTargets(targets map[string]work.Target) {
	gl, ok := r.byDialect["gitlab"]
	if r.gitlabCreds == nil || !ok {
		return
	}
	ids := forgeIDsOf(r.registry, gl.provider)
	var projects []string
	for _, t := range targets {
		forge := t.Forge
		if forge == "" {
			forge = r.defaultForge
		}
		if slices.Contains(ids, forge) && t.Owner != "" && t.Repo != "" {
			projects = append(projects, t.Owner+"/"+t.Repo)
		}
	}
	sort.Strings(projects)
	r.gitlabCreds.Projects = func(context.Context) ([]string, error) { return projects, nil }
}

func workerURLFromEnv(dialect string) string {
	return trimSlash(map[string]string{
		"forgejo": os.Getenv("PLOEG_FORGEJO_WORKER_URL"),
		"gitlab":  os.Getenv("PLOEG_GITLAB_WORKER_URL"),
		"github":  os.Getenv("PLOEG_GITHUB_WORKER_URL"),
	}[dialect])
}

func (r releaseForges) dialects() []string {
	out := make([]string, 0, len(r.byDialect))
	for d := range r.byDialect {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (r releaseForges) multiForge() bool { return len(r.byDialect) > 1 }

// requireMinting refuses a release that serves more than one forge unless
// every one of them mints per-Run credentials for readers and writers: no
// worker pod of such a release holds a standing forge credential, so a forge
// that cannot mint has no credential to hand a Run at all.
func (r releaseForges) requireMinting() error {
	if !r.multiForge() {
		return nil
	}
	var lacks []error
	for _, dialect := range r.dialects() {
		switch dialect {
		case "forgejo":
			if _, mints := r.forgejoCreds.(*forgebroker.Forgejo); !mints {
				lacks = append(lacks, errors.New("forge forgejo cannot mint per-Run credentials: set PLOEG_FORGEJO_BOT_PASSWORD (chart executor.forgejo.botPasswordSecret) on Forgejo 15 or later"))
			}
		case "github":
			if r.githubApp == nil {
				lacks = append(lacks, errors.New("forge github cannot mint per-Run credentials: configure a GitHub App with PLOEG_GITHUB_APP_ID and PLOEG_GITHUB_APP_PRIVATE_KEY (chart tracker.github.appId and appPrivateKeySecret)"))
			}
		case "gitlab":
			if r.gitlabCreds == nil {
				lacks = append(lacks, errors.New("forge gitlab cannot mint per-Run credentials: set PLOEG_GITLAB_MINT_PER_RUN=on with a group service account's token in PLOEG_GITLAB_TOKEN (chart executor.gitlab.mintPerRun)"))
			}
		default:
			lacks = append(lacks, fmt.Errorf("forge %s cannot mint per-Run credentials, so it can only be the only forge of its release", dialect))
		}
	}
	if len(lacks) == 0 {
		return nil
	}
	return fmt.Errorf("this release serves %d forges (%s) and each must mint per-Run credentials (ADR-0192): %w",
		len(r.byDialect), strings.Join(r.dialects(), ", "), errors.Join(lacks...))
}

// credentials is the broker and sweeper a release mints with. A release with
// one forge and no GitHub App keeps exactly the broker it had before
// ADR-0192; any other is routed by forge id, so no mint reaches another
// forge's broker.
func (r releaseForges) credentials(log *slog.Logger) (forgebroker.Broker, forgebroker.Sweeper) {
	if r.githubApp == nil && r.gitlabCreds == nil && !r.multiForge() {
		return r.forgejoCreds, r.forgejoSweeper
	}
	router := &forgebroker.Router{Forgejo: r.forgejoCreds, ForgejoSweeper: r.forgejoSweeper, DefaultForge: r.defaultForge}
	if fj, ok := r.byDialect["forgejo"]; ok {
		router.ForgejoForges = forgeIDsOf(r.registry, fj.provider)
	}
	if gh, ok := r.byDialect["github"]; ok && r.githubApp != nil {
		router.GitHub = &forgebroker.GitHubApp{App: r.githubApp}
		router.GitHubForges = forgeIDsOf(r.registry, gh.provider)
		log.Info("per-run GitHub App installation tokens enabled", "forges", router.GitHubForges)
	}
	if gl, ok := r.byDialect["gitlab"]; ok && r.gitlabCreds != nil {
		router.GitLab = r.gitlabCreds
		router.GitLabForges = forgeIDsOf(r.registry, gl.provider)
	}
	return router, router
}

// directory is the forge registry as a claim resolves it. A forge's worker
// URL rides on the claim when it is configured, and in a release with more
// than one forge it defaults to ploegd's own URL for that forge; otherwise the
// worker keeps its FORGE_URL. A release without a forge provider resolves
// nothing, and its workers keep their env forge as before ADR-0192. A
// forge served only through runtime connections resolves its instance id
// alone, at the instance's URL.
func (r releaseForges) directory() *httpapi.ForgeDirectory {
	d := &httpapi.ForgeDirectory{Default: r.defaultForge, Forges: map[string]httpapi.ClaimForge{}, Runtime: r.runtime}
	for id, p := range r.registry {
		dialect := p.Name()
		f, static := r.byDialect[dialect]
		if !static {
			continue
		}
		url := f.workerURL
		if url == "" && r.multiForge() {
			url = f.url
		}
		d.Forges[id] = httpapi.ClaimForge{Dialect: dialect, URL: url, PloegdURL: f.url}
	}
	if len(d.Forges) == 0 && len(d.Runtime) == 0 {
		return nil
	}
	return d
}

func forgeIDsOf(forges map[string]provider.ForgeProvider, fp provider.ForgeProvider) []string {
	var ids []string
	for id, candidate := range forges {
		if candidate == fp {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

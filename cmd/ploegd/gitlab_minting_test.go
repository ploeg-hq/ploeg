package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestAGitLabForgeThatMintsPerRunMayServeBesideAnotherForge(t *testing.T) {
	minting := &forgebroker.Forgejo{BaseURL: "https://forgejo.example", Bot: "agent-builder", Password: "pw"}
	release := releaseOf("forgejo", minting, nil, forgejoAt("https://forgejo.example"), gitlabAt("https://gitlab.example"))
	t.Setenv("PLOEG_GITLAB_MINT_PER_RUN", "on")
	t.Setenv("PLOEG_GITLAB_TOKEN", "sa-pat-1")
	gl, err := gitlabMinter(slog.New(slog.DiscardHandler), release.byDialect)
	if err != nil || gl == nil || gl.APIURL != "https://gitlab.example/api/v4" {
		t.Fatalf("minter = %+v, %v", gl, err)
	}
	release.gitlabCreds = gl
	if err := release.requireMinting(); err != nil {
		t.Fatalf("a release whose GitLab mints was refused: %v", err)
	}
	broker, _ := release.credentials(slog.New(slog.DiscardHandler))
	router, ok := broker.(*forgebroker.Router)
	if !ok || router.GitLab != gl || strings.Join(router.GitLabForges, ",") != "gitlab" {
		t.Fatalf("router = %+v", broker)
	}
	release.sweepGitLabTargets(map[string]work.Target{"a": {Forge: "gitlab", Owner: "acme/team", Repo: "app"}, "b": {Owner: "acme", Repo: "forgejo-app"}})
	projects, _ := gl.Projects(context.Background())
	if strings.Join(projects, ",") != "acme/team/app" {
		t.Fatalf("the sweep lists %v, want the static GitLab targets alone", projects)
	}
	t.Setenv("PLOEG_GITLAB_TOKEN", "")
	if _, err := gitlabMinter(slog.New(slog.DiscardHandler), release.byDialect); err == nil {
		t.Fatal("minting was switched on without a token")
	}
	t.Setenv("PLOEG_GITLAB_MINT_PER_RUN", "")
	if gl, err := gitlabMinter(slog.New(slog.DiscardHandler), release.byDialect); gl != nil || err != nil {
		t.Fatalf("minting is opt-in: %v, %v", gl, err)
	}
}

func TestAGitLabOAuthConnectionMintsNothing(t *testing.T) {
	info := connection.ForgeConnectionInfo{ID: "gitlab-person", Kind: "gitlab", Runtime: true, Method: connection.MethodOAuth}
	_, err := gitlabConnectionBroker(context.Background(), nil, info, nil)
	if !errors.Is(err, forgebroker.ErrConnectionCannotMint) || !strings.Contains(err.Error(), "gitlab_service_account") {
		t.Fatalf("err = %v", err)
	}
}

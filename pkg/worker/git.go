package worker

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// cloneArgs builds the git clone invocation: a configured base branch is
// cloned explicitly; unset means the repo's default branch — which may be a
// stale stub, so a target should pin its base branch (VIK-589).
func cloneArgs(baseBranch, cloneURL, cloneDir string) []string {
	args := []string{"clone", "--depth", "50"}
	if baseBranch != "" {
		args = append(args, "--branch", baseBranch)
	}
	return append(args, cloneURL, cloneDir)
}

// fetchBranchArgs brings the branch under review into a clone that was made
// from the base branch.
//
// A reader used to receive none of the work it was asked to review: the clone
// above is `--depth 50 --branch <base>`, and --depth implies --single-branch,
// so the writer's branch was not in the repository at all — while the review
// prompt claimed "the repository checkout is your working directory, on branch
// <shift branch>". Fetching it as a local ref of the same name keeps the base
// present too, so a reader can diff the two.
func fetchBranchArgs(branch string) []string {
	return []string{"fetch", "--depth", "50", "origin", branch + ":" + branch}
}

func plainURL(base, owner, repo string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid forge URL")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = "/" + owner + "/" + repo + ".git"
	return u.String(), nil
}

func gitAuthenticationEnvironment(repositoryURL, token string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if token != "" {
		header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+token))
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+repositoryURL+".extraheader", "GIT_CONFIG_VALUE_0="+header)
	}
	return env
}

const gitWaitDelay = 5 * time.Second

func runGit(ctx context.Context, dir, repositoryURL, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = gitWaitDelay
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, gitAuthenticationEnvironment(repositoryURL, token)...)
	return cmd.CombinedOutput()
}

// scrubSecrets removes forge credentials from the environment handed to a
// READING run's agent.
//
// It matches on VALUE, not on variable name, deliberately: the token reaches
// the pod as AGENT_BUILDER_TOKEN today, but any future variable carrying the
// same string would leak it just as well, and a name list silently rots. A
// writing run is returned unchanged — it has legitimate work to push.
func scrubSecrets(env []string, writes bool, secrets ...string) []string {
	if writes {
		return env
	}
	drop := map[string]bool{}
	for _, s := range secrets {
		if strings.TrimSpace(s) != "" {
			drop[s] = true
		}
	}
	if len(drop) == 0 {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if _, val, ok := strings.Cut(kv, "="); ok && drop[val] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

package claudeplugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// FetchTimeout bounds fetching one marketplace.
const FetchTimeout = 60 * time.Second

// Fetcher checks a marketplace out at its pinned commit into dir.
type Fetcher func(ctx context.Context, m Marketplace, dir string) error

// GitFetch fetches m with git, without any credential, and checks out
// m.Commit. It refuses a checkout whose HEAD is not m.Commit.
func GitFetch(ctx context.Context, m Marketplace, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{
			"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null",
			"-c", "protocol.file.allow=always", "-c", "advice.detachedHead=false",
		}, args...)...)
		cmd.Dir = dir
		cmd.Env = gitEnv(dir)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, lastLine(out.String()))
		}
		return nil
	}
	if err := git("init", "-q"); err != nil {
		return err
	}
	if err := git("fetch", "-q", "--depth", "1", m.URL, m.Commit); err != nil {
		if err := git("fetch", "-q", m.URL); err != nil {
			return err
		}
	}
	if err := git("checkout", "-q", "--detach", m.Commit); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir, cmd.Env = dir, gitEnv(dir)
	head, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git rev-parse: %w", err)
	}
	if got := strings.TrimSpace(string(head)); got != m.Commit {
		return fmt.Errorf("checked out %s, not the pinned %s", got, m.Commit)
	}
	return os.RemoveAll(dir + "/.git")
}

func gitEnv(dir string) []string {
	env := []string{"HOME=" + dir, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ASKPASS=/bin/false"}
	for _, k := range []string{"PATH", "LANG", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

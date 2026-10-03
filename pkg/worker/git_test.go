package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A reader used to be handed a clone that could push: AGENT_BUILDER_TOKEN is
// mandatory on every worker pod and reached the agent through os.Environ(),
// while the prompt told it no write credential existed.
func TestScrubSecrets_ReadingRunLosesTheForgeToken(t *testing.T) {
	const tok = "s3cr3t-forge-token"
	env := []string{"PATH=/usr/bin", "AGENT_BUILDER_TOKEN=" + tok, "LLM_MODEL=x"}

	got := scrubSecrets(env, false, tok, "")
	for _, kv := range got {
		if strings.Contains(kv, tok) {
			t.Fatalf("reading run still carries the forge token: %q", kv)
		}
	}
	if len(got) != 2 {
		t.Errorf("scrubbed %d vars, want 2 kept (PATH, LLM_MODEL): %v", len(got), got)
	}
}

// The same scrub must NOT fire for a writer, which has work to push.
func TestScrubSecrets_WritingRunKeepsIt(t *testing.T) {
	const tok = "s3cr3t-forge-token"
	env := []string{"AGENT_BUILDER_TOKEN=" + tok}
	if got := scrubSecrets(env, true, tok); len(got) != 1 {
		t.Fatalf("a writer lost its credential: %v", got)
	}
}

// Matching is by VALUE, so a differently-named variable carrying the same
// token is caught too — a name list would rot silently.
func TestScrubSecrets_MatchesByValueNotName(t *testing.T) {
	const tok = "s3cr3t-forge-token"
	env := []string{"SOME_OTHER_NAME=" + tok}
	if got := scrubSecrets(env, false, tok); len(got) != 0 {
		t.Errorf("token survived under another name: %v", got)
	}
}

func TestPlainURL_HasNoCredentials(t *testing.T) {
	got, err := plainURL("http://forgejo:3000", "webgrip", "erfbeeld")
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://forgejo:3000/webgrip/erfbeeld.git"; got != want {
		t.Errorf("plainURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "@") {
		t.Errorf("plainURL carries credentials: %q", got)
	}
}

func TestFetchBranchArgs_BringsTheBranchUnderReview(t *testing.T) {
	got := strings.Join(fetchBranchArgs("agent/vik-624"), " ")
	if want := "fetch --depth 50 origin agent/vik-624:agent/vik-624"; got != want {
		t.Errorf("fetchBranchArgs = %q, want %q", got, want)
	}
}

func TestGitAuthenticationNeverPersistsInClone(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "source.git")
	clone := filepath.Join(base, "clone")
	const token = "synthetic-fixture-forge-credential"
	const repositoryURL = "https://forge.example/team/repo.git"
	for _, args := range [][]string{{"init", "--bare", source}, {"clone", source, clone}} {
		if output, err := runGit(ctx, "", repositoryURL, token, args...); err != nil {
			t.Fatalf("git failed: %v %s", err, output)
		}
	}
	config, err := os.ReadFile(filepath.Join(clone, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || strings.Contains(string(config), "extraheader") || strings.Contains(string(config), "Authorization") {
		t.Fatal("Git persisted transient authentication")
	}
	out, err := runGit(ctx, clone, repositoryURL, token, "config", "--get-urlmatch", "http.extraheader", repositoryURL)
	if err != nil || !strings.HasPrefix(string(out), "Authorization: Basic ") {
		t.Fatal("repository authentication was unavailable")
	}
	for _, other := range []string{"https://other.example/team/repo.git", "https://forge.example/team/other.git"} {
		if out, err := runGit(ctx, clone, repositoryURL, token, "config", "--get-urlmatch", "http.extraheader", other); err == nil || len(out) != 0 {
			t.Fatal("Git authentication escaped repository URL scope")
		}
	}
}

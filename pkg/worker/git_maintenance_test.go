package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// automaticMaintenanceOff keeps Git from starting background maintenance in
// the temporary repositories these tests create. Recent Git detaches that
// maintenance after a commit or fetch, and it can still be writing into .git
// when t.TempDir removes the directory (ploeg-hq/ploeg#46).
var automaticMaintenanceOff = [][2]string{{"maintenance.auto", "false"}, {"gc.auto", "0"}}

func init() {
	for _, setting := range automaticMaintenanceOff {
		testGitConfig = append(testGitConfig, "-c", setting[0]+"="+setting[1])
	}
	env := map[string]string{"GIT_CONFIG_COUNT": strconv.Itoa(len(automaticMaintenanceOff))}
	for i, setting := range automaticMaintenanceOff {
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = setting[0]
		env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = setting[1]
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
}

func TestTestRepositoriesNeverStartAutomaticMaintenance(t *testing.T) {
	repo := t.TempDir()
	if out, err := runGit(context.Background(), repo, "", "", "init", "-q"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, setting := range automaticMaintenanceOff {
		out, err := runGit(context.Background(), repo, "", "", "config", "--get", setting[0])
		if err != nil || strings.TrimSpace(string(out)) != setting[1] {
			t.Errorf("worker git sees %s = %q (%v), want %q", setting[0], strings.TrimSpace(string(out)), err, setting[1])
		}
		fixture := exec.Command("git", "config", "--get", setting[0])
		fixture.Dir = repo
		fixture.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err = fixture.Output()
		if err != nil || strings.TrimSpace(string(out)) != setting[1] {
			t.Errorf("fixture git sees %s = %q (%v), want %q", setting[0], strings.TrimSpace(string(out)), err, setting[1])
		}
	}
}

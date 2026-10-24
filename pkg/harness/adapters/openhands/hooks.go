package openhands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// RepositoryHooksPath is where OpenHands looks for a hooks file inside the
// working directory, before it looks in the home directory.
const RepositoryHooksPath = ".openhands/hooks.json"

// ErrRepositoryHooks is returned, wrapped, when the clone ships a hooks file
// OpenHands would run.
var ErrRepositoryHooks = errors.New("the repository ships an OpenHands hooks file")

// RefuseRepositoryHooks fails when repoDir holds .openhands/hooks.json.
//
// The OpenHands CLI 1.16.0 (SDK 1.21.0, the frozen release ADR-0118 records)
// loads hooks with HookConfig.load(working_dir=<the clone>), which takes
// <clone>/.openhands/hooks.json first and ~/.openhands/hooks.json only when the
// clone has none. Each hook is a shell command OpenHands runs on session start,
// on every prompt and before and after every tool call, outside the ACP
// permission request, so a repository could run its own code in every Run and
// under every permission mode. Neither the CLI nor the SDK has a setting that
// skips the working directory, and editing the clone would put the change in
// the Run's commit, so both OpenHands harnesses refuse the Run instead
// (ADR-0148).
//
// The check follows the lookup's own rule, Path.exists(): a missing file, a
// .openhands that is not a directory, or a dangling symlink is absent; any
// other error to stat it refuses, because OpenHands might still read it.
func RefuseRepositoryHooks(repoDir string) error {
	if repoDir == "" {
		return nil
	}
	path := filepath.Join(repoDir, filepath.FromSlash(RepositoryHooksPath))
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%w (%s): OpenHands would run its commands inside the Run; remove it or run this repository with another harness", ErrRepositoryHooks, RepositoryHooksPath)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ELOOP):
		return nil
	default:
		return fmt.Errorf("%w (%s): cannot tell whether OpenHands would load it: %v", ErrRepositoryHooks, RepositoryHooksPath, err)
	}
}

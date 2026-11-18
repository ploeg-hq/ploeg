package openhands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// RepositoryHooksPath is where OpenHands looks for a hooks file inside the
// working directory, before it looks in the home directory.
const RepositoryHooksPath = ".openhands/hooks.json"

// RepositoryHooksDir is the directory RepositoryHooksPath lives in. When it is
// a symbolic link, OpenHands follows it to the hooks file.
const RepositoryHooksDir = ".openhands"

const maxRepositoryHooksBytes = 1 << 20

// ErrRepositoryHooks is returned, wrapped, when the clone ships a hooks entry
// Ploeg cannot set aside and put back exactly, so the Run does not start.
var ErrRepositoryHooks = errors.New("the repository's OpenHands hooks file cannot be set aside")

// RepositoryHooks is a clone's OpenHands hooks file, set aside for one Run.
//
// The OpenHands CLI 1.16.0 (SDK 1.21.0, the frozen release ADR-0118 records)
// loads hooks with HookConfig.load(working_dir=<the clone>) when a session
// starts: it takes <clone>/.openhands/hooks.json when Path.exists() and
// ~/.openhands/hooks.json only when the clone has none. Each hook is a shell
// command OpenHands runs on session start, on every prompt and around every
// tool call, outside the ACP permission request, and neither the CLI nor the
// SDK has a setting that skips the working directory. So the file is moved out
// of the clone before the harness starts, any hooks file found there before a
// session starts is removed, and Restore puts the shipped file back byte for
// byte once the harness has stopped (ADR-0148, amended 2026-11-18).
//
// A tracked entry is marked skip-worktree before it is removed, so the agent's
// own git add or commit -a leaves it unchanged in the index; the worker's
// forge proxy refuses a push that changes it all the same.
type RepositoryHooks struct {
	root    string
	entry   string
	data    []byte
	mode    fs.FileMode
	target  string
	symlink bool
	written []string
}

// SetAsideRepositoryHooks moves repoDir's OpenHands hooks file out of the
// clone and keeps it in memory for Restore. It returns a RepositoryHooks even
// when the clone ships none, so Restore still finds a file the agent wrote.
func SetAsideRepositoryHooks(repoDir string) (*RepositoryHooks, error) {
	h := &RepositoryHooks{root: repoDir}
	if repoDir == "" {
		return h, nil
	}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return nil, fmt.Errorf("%w: open the clone: %v", ErrRepositoryHooks, err)
	}
	defer root.Close()
	if err := h.remember(root); err != nil {
		return nil, err
	}
	if h.entry == "" {
		return h, nil
	}
	if err := keepInIndex(repoDir, h.entry); err != nil {
		return nil, fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, h.entry, err)
	}
	if err := root.Remove(h.entry); err != nil {
		return nil, fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, h.entry, err)
	}
	return h, nil
}

// Shipped is the path set aside, empty when the clone shipped no hooks file.
func (h *RepositoryHooks) Shipped() string {
	if h == nil {
		return ""
	}
	return h.entry
}

func (h *RepositoryHooks) remember(root *os.Root) error {
	dir, err := root.Lstat(RepositoryHooksDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, RepositoryHooksDir, err)
	case dir.Mode()&fs.ModeSymlink != 0:
		return h.rememberSymlink(root, RepositoryHooksDir)
	case !dir.IsDir():
		return nil
	}
	file, err := root.Lstat(RepositoryHooksPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, RepositoryHooksPath, err)
	case file.Mode()&fs.ModeSymlink != 0:
		return h.rememberSymlink(root, RepositoryHooksPath)
	case !file.Mode().IsRegular():
		return fmt.Errorf("%w (%s): it is a %s, not a file", ErrRepositoryHooks, RepositoryHooksPath, file.Mode().Type())
	case file.Size() > maxRepositoryHooksBytes:
		return fmt.Errorf("%w (%s): it is larger than %d bytes", ErrRepositoryHooks, RepositoryHooksPath, maxRepositoryHooksBytes)
	}
	f, err := root.Open(RepositoryHooksPath)
	if err != nil {
		return fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, RepositoryHooksPath, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRepositoryHooksBytes+1))
	if err != nil {
		return fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, RepositoryHooksPath, err)
	}
	if len(data) > maxRepositoryHooksBytes {
		return fmt.Errorf("%w (%s): it is larger than %d bytes", ErrRepositoryHooks, RepositoryHooksPath, maxRepositoryHooksBytes)
	}
	h.entry, h.data, h.mode = RepositoryHooksPath, data, file.Mode().Perm()
	return nil
}

func (h *RepositoryHooks) rememberSymlink(root *os.Root, name string) error {
	target, err := root.Readlink(name)
	if err != nil {
		return fmt.Errorf("%w (%s): %v", ErrRepositoryHooks, name, err)
	}
	h.entry, h.target, h.symlink = name, target, true
	return nil
}

// BeforeSession removes a hooks file that appeared in the clone since it was
// set aside, so a session that starts now loads none. It returns what it
// removed.
func (h *RepositoryHooks) BeforeSession() ([]string, error) {
	if h == nil || h.root == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(h.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	removed, err := clearHooks(root)
	h.noteWritten(removed...)
	return removed, err
}

// Restore removes any hooks file the agent left in the clone and puts the
// shipped one back as it was. Call it once the harness has stopped. It returns
// every hooks path the agent wrote during the Run, including those
// BeforeSession already removed.
func (h *RepositoryHooks) Restore() ([]string, error) {
	if h == nil || h.root == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(h.root)
	if err != nil {
		return h.written, err
	}
	defer root.Close()
	removed, err := clearHooks(root)
	h.noteWritten(removed...)
	if err != nil || h.entry == "" {
		return h.written, err
	}
	if h.entry == RepositoryHooksDir {
		if _, err := root.Lstat(RepositoryHooksDir); err == nil {
			h.noteWritten(RepositoryHooksDir)
			if err := root.RemoveAll(RepositoryHooksDir); err != nil {
				return h.written, err
			}
		}
		return h.written, root.Symlink(h.target, RepositoryHooksDir)
	}
	if err := h.ensureHooksDirectory(root); err != nil {
		return h.written, err
	}
	if h.symlink {
		return h.written, root.Symlink(h.target, RepositoryHooksPath)
	}
	return h.written, writeExactly(root, RepositoryHooksPath, h.data, h.mode)
}

func (h *RepositoryHooks) noteWritten(paths ...string) {
	for _, p := range paths {
		if !slices.Contains(h.written, p) {
			h.written = append(h.written, p)
		}
	}
}

func (h *RepositoryHooks) ensureHooksDirectory(root *os.Root) error {
	info, err := root.Lstat(RepositoryHooksDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return root.Mkdir(RepositoryHooksDir, 0o755)
	case err != nil:
		return err
	case info.IsDir():
		return nil
	}
	h.noteWritten(RepositoryHooksDir)
	if err := root.Remove(RepositoryHooksDir); err != nil {
		return err
	}
	return root.Mkdir(RepositoryHooksDir, 0o755)
}

// clearHooks removes whatever OpenHands could load as the clone's hooks file:
// a .openhands symbolic link, or any entry at .openhands/hooks.json. It never
// follows a link, so it cannot reach outside the clone.
func clearHooks(root *os.Root) ([]string, error) {
	dir, err := root.Lstat(RepositoryHooksDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case dir.Mode()&fs.ModeSymlink != 0:
		return []string{RepositoryHooksDir}, root.Remove(RepositoryHooksDir)
	case !dir.IsDir():
		return nil, nil
	}
	_, err = root.Lstat(RepositoryHooksPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return []string{RepositoryHooksPath}, root.RemoveAll(RepositoryHooksPath)
}

func writeExactly(root *os.Root, name string, data []byte, mode fs.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

// keepInIndex marks a tracked entry skip-worktree in the clone's index, so its
// absence from the working tree is not a change the agent's git stages. It
// runs before the harness starts, while the clone's git configuration is still
// the worker's, and switches off hooks and the filesystem monitor all the same.
// An entry git does not track, or a directory that is not a repository, needs
// nothing.
func keepInIndex(repoDir, entry string) error {
	tracked, err := isolatedGit(repoDir, "ls-files", "-z", "--", entry).Output()
	if err != nil || len(bytes.TrimRight(tracked, "\x00")) == 0 {
		return nil
	}
	if out, err := isolatedGit(repoDir, "update-index", "--skip-worktree", "--", entry).CombinedOutput(); err != nil {
		return fmt.Errorf("git update-index --skip-worktree: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isolatedGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	return cmd
}

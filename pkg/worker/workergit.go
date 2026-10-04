package worker

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const workerGitDirPattern = "worker-git-"

type workerRepository struct {
	gitDir   string
	workTree string
	remote   string
	token    string
}

type checkoutRefs struct {
	head string
	refs map[string]string
}

func (s checkoutRefs) objectFormat() string {
	if len(s.head) == 64 {
		return "sha256"
	}
	return "sha1"
}

func openWorkerRepository(ctx context.Context, checkoutDir, remote, token string) (*workerRepository, error) {
	workTree, err := filepath.Abs(checkoutDir)
	if err != nil {
		return nil, err
	}
	refs, err := readCheckoutRefs(filepath.Join(workTree, ".git"))
	if err != nil {
		return nil, fmt.Errorf("read the checkout's refs: %w", err)
	}
	gitDir, err := os.MkdirTemp(filepath.Dir(workTree), workerGitDirPattern)
	if err != nil {
		return nil, err
	}
	repo := &workerRepository{gitDir: gitDir, workTree: workTree, remote: remote, token: token}
	if err := repo.seed(ctx, refs); err != nil {
		repo.remove()
		return nil, err
	}
	return repo, nil
}

func (r *workerRepository) remove() {
	_ = os.RemoveAll(r.gitDir)
}

func (r *workerRepository) seed(ctx context.Context, refs checkoutRefs) error {
	if out, err := r.local(ctx, "", "init", "--quiet", "--bare", "--template=", "--object-format="+refs.objectFormat()).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %v: %s", err, tail(out, 400))
	}
	cloneObjects := filepath.Join(r.workTree, ".git", "objects")
	info := filepath.Join(r.gitDir, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(cloneObjects+"\n"), 0o600); err != nil {
		return err
	}
	shallow, err := readRegularFile(filepath.Join(r.workTree, ".git", "shallow"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := os.WriteFile(filepath.Join(r.gitDir, "shallow"), shallow, 0o600); err != nil {
			return err
		}
	}
	var updates bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(refs.refs)) {
		if strings.HasPrefix(name, "refs/heads/") || strings.HasPrefix(name, "refs/remotes/") {
			fmt.Fprintf(&updates, "update %s\x00%s\x00\x00", name, refs.refs[name])
		}
	}
	update := r.local(ctx, "", "update-ref", "-z", "--stdin")
	update.Stdin = &updates
	if out, err := update.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref: %v: %s", err, tail(out, 400))
	}
	if out, err := r.local(ctx, "", "update-ref", "--no-deref", "HEAD", refs.head).CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref HEAD: %v: %s", err, tail(out, 400))
	}
	if out, err := r.local(ctx, "", "read-tree", "HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("git read-tree: %v: %s", err, tail(out, 400))
	}
	return nil
}

func (r *workerRepository) local(ctx context.Context, workTree string, args ...string) *exec.Cmd {
	return r.command(ctx, workTree, "", args...)
}

func (r *workerRepository) networked(ctx context.Context, args ...string) *exec.Cmd {
	return r.command(ctx, "", r.token, args...)
}

func (r *workerRepository) command(ctx context.Context, workTree, token string, args ...string) *exec.Cmd {
	location := []string{"--git-dir=" + r.gitDir}
	cmd := exec.CommandContext(ctx, "git")
	cmd.Dir = r.gitDir
	if workTree != "" {
		location = append(location, "--work-tree="+workTree)
		cmd.Dir = workTree
	}
	cmd.Args = slices.Concat([]string{"git"}, testGitConfig, location, args)
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = append(processEnvironment(), isolatedGitEnvironment(r.remote, token)...)
	return cmd
}

func isolatedGitEnvironment(remote, token string) []string {
	settings := [][2]string{
		{"core.hooksPath", "/dev/null"},
		{"core.fsmonitor", "false"},
		{"submodule.recurse", "false"},
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
	}
	if token != "" {
		settings = append(settings, [2]string{"http." + remote + ".extraheader", forgeAuthorizationHeader(token)})
	}
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(settings))}
	for i, setting := range settings {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, setting[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting[1]))
	}
	return env
}

func (r *workerRepository) branchHead(ctx context.Context, branch string) branchHead {
	readCtx, cancel := context.WithTimeout(ctx, branchReadTimeout)
	defer cancel()
	out, err := r.networked(readCtx, "ls-remote", "--exit-code", r.remote, "refs/heads/"+branch).CombinedOutput()
	return parseBranchHead(branch, out, err)
}

func (r *workerRepository) fetchBranch(ctx context.Context, branch, into string, extra ...string) error {
	args := slices.Concat([]string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules"}, extra,
		[]string{r.remote, "+refs/heads/" + branch + ":" + into})
	if out, err := r.networked(ctx, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s: %v: %s", branch, err, strings.TrimSpace(tail(out, 400)))
	}
	return nil
}

func (r *workerRepository) commitIsLocal(ctx context.Context, commit string) bool {
	return r.local(ctx, "", "cat-file", "-e", commit+"^{commit}").Run() == nil
}

func (r *workerRepository) output(ctx context.Context, args ...string) ([]byte, error) {
	return r.local(ctx, r.workTree, args...).CombinedOutput()
}

func (r *workerRepository) inspect(ctx context.Context, start, publishedHead string) (checkoutChanges, error) {
	var changes checkoutChanges
	out, err := r.output(ctx, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return changes, fmt.Errorf("git status: %v: %s", err, tail(out, 400))
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) > 3 {
			changes.files = append(changes.files, line[3:])
		}
	}
	args := []string{"rev-list", "--oneline", "HEAD", "--branches", "--not", start, "--remotes"}
	if publishedHead != "" && r.commitIsLocal(ctx, publishedHead) {
		args = append(args, publishedHead)
	}
	out, err = r.local(ctx, "", args...).CombinedOutput()
	if err != nil {
		return changes, fmt.Errorf("git rev-list: %v: %s", err, tail(out, 400))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			changes.commits = append(changes.commits, line)
		}
	}
	return changes, nil
}

func (r *workerRepository) exportBranch(ctx context.Context, branch string) (string, error) {
	const ref = "refs/ploeg/gate"
	if err := r.fetchBranch(ctx, branch, ref, "--depth", "50"); err != nil {
		return "", err
	}
	tree := filepath.Join(r.gitDir, "export")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return "", err
	}
	if out, err := r.local(ctx, "", "read-tree", ref).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git read-tree: %v: %s", err, tail(out, 1000))
	}
	if out, err := r.local(ctx, tree, "checkout-index", "--all", "--force").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git checkout-index: %v: %s", err, tail(out, 1000))
	}
	return tree, nil
}

func readCheckoutRefs(gitDir string) (checkoutRefs, error) {
	all := map[string]string{}
	packed, err := readRegularFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return checkoutRefs{}, err
	}
	for _, line := range strings.Split(string(packed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		object, name, ok := strings.Cut(line, " ")
		if !ok || !isObjectName(object) {
			return checkoutRefs{}, fmt.Errorf("unreadable packed-refs line %q", line)
		}
		all[name] = object
	}
	refsDir := filepath.Join(gitDir, "refs")
	err = filepath.WalkDir(refsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("ref %s is not a regular file", path)
		}
		content, err := readRegularFile(path)
		if err != nil {
			return err
		}
		value := strings.TrimSpace(string(content))
		if strings.HasPrefix(value, "ref: ") {
			return nil
		}
		if !isObjectName(value) {
			return fmt.Errorf("ref %s does not hold an object name", path)
		}
		rel, err := filepath.Rel(gitDir, path)
		if err != nil {
			return err
		}
		all[filepath.ToSlash(rel)] = value
		return nil
	})
	if err != nil {
		return checkoutRefs{}, err
	}
	headFile, err := readRegularFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return checkoutRefs{}, err
	}
	head := strings.TrimSpace(string(headFile))
	if target, symbolic := strings.CutPrefix(head, "ref: "); symbolic {
		resolved, ok := all[target]
		if !ok {
			return checkoutRefs{}, fmt.Errorf("HEAD names %s, which holds no commit", target)
		}
		head = resolved
	}
	if !isObjectName(head) {
		return checkoutRefs{}, fmt.Errorf("HEAD does not hold an object name")
	}
	for name, object := range all {
		if len(object) != len(head) {
			return checkoutRefs{}, fmt.Errorf("ref %s mixes object formats", name)
		}
	}
	return checkoutRefs{head: head, refs: all}, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func isObjectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

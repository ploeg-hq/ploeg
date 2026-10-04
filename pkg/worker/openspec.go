package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var openSpecBinary = "openspec"

const (
	openSpecTimeout        = 90 * time.Second
	maxOpenSpecBriefBytes  = 12000
	maxOpenSpecFileBytes   = 5000
	maxOpenSpecGateBytes   = 6000
	maxOpenSpecSearchDepth = 6
)

var openSpecSkippedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "archive": true}

type openSpecChange struct {
	ID      string
	Root    string
	RelRoot string
	repo    string
}

func (c openSpecChange) relChangeDir() string {
	return filepath.ToSlash(filepath.Join(c.RelRoot, "openspec", "changes", c.ID))
}

func locateOpenSpecChange(repoDir, id string) (openSpecChange, error) {
	if abs, err := filepath.Abs(repoDir); err == nil {
		repoDir = abs
	}
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = resolved
	}
	var roots []string
	err := filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(repoDir, p)
		if p != repoDir && (openSpecSkippedDirs[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= maxOpenSpecSearchDepth) {
			return filepath.SkipDir
		}
		if d.Name() != "openspec" {
			return nil
		}
		if realDir(filepath.Join(p, "changes")) && realDir(filepath.Join(p, "changes", id)) {
			roots = append(roots, filepath.Dir(p))
		}
		return filepath.SkipDir
	})
	if err != nil {
		return openSpecChange{}, fmt.Errorf("search the repository for OpenSpec change %q: %w", id, err)
	}
	switch len(roots) {
	case 0:
		return openSpecChange{}, fmt.Errorf("the Work Item names OpenSpec change %q, but no openspec/changes/%s directory exists in the checkout", id, id)
	case 1:
	default:
		rels := make([]string, len(roots))
		for i, r := range roots {
			rels[i] = relativeTo(repoDir, r)
		}
		return openSpecChange{}, fmt.Errorf("OpenSpec change %q exists under more than one root (%s); the Run cannot tell which one the Work Item means", id, strings.Join(rels, ", "))
	}
	return openSpecChange{ID: id, Root: roots[0], RelRoot: relativeTo(repoDir, roots[0]), repo: repoDir}, nil
}

func realDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}

func relativeTo(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func openSpecEnvironment(home string) []string {
	env := []string{"HOME=" + home, "OPENSPEC_TELEMETRY=0", "DO_NOT_TRACK=1", "NO_COLOR=1", "CI=1"}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

var errOpenSpecUnavailable = errors.New("the openspec CLI is not on the worker's PATH")

func runOpenSpec(ctx context.Context, dir, home string, args ...string) (stdout []byte, stderr string, err error) {
	bin, err := exec.LookPath(openSpecBinary)
	if err != nil {
		return nil, "", errOpenSpecUnavailable
	}
	runCtx, cancel := context.WithTimeout(ctx, openSpecTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = openSpecEnvironment(home)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	if runCtx.Err() != nil {
		err = fmt.Errorf("openspec %s timed out after %s", args[0], openSpecTimeout)
	}
	return out.Bytes(), errOut.String(), err
}

type openSpecApply struct {
	ChangeName   string              `json:"changeName"`
	SchemaName   string              `json:"schemaName"`
	ContextFiles map[string][]string `json:"contextFiles"`
	Progress     struct {
		Total     int `json:"total"`
		Complete  int `json:"complete"`
		Remaining int `json:"remaining"`
	} `json:"progress"`
	Tasks []struct {
		Description string `json:"description"`
		Done        bool   `json:"done"`
	} `json:"tasks"`
	State       string `json:"state"`
	Instruction string `json:"instruction"`
}

func openSpecBriefFor(ctx context.Context, ch openSpecChange, home string) harness.OpenSpecBrief {
	brief := harness.OpenSpecBrief{Change: ch.ID, Root: ch.RelRoot}
	out, stderr, err := runOpenSpec(ctx, ch.Root, home, "instructions", "apply", "--change", ch.ID, "--json")
	var apply openSpecApply
	if err == nil {
		err = json.Unmarshal(out, &apply)
	}
	if err == nil {
		brief.Source = harness.OpenSpecSourceCLI
		brief.Brief = capText(renderApplyBrief(ch, apply), maxOpenSpecBriefBytes)
		return brief
	}
	reason := err.Error()
	if s := strings.TrimSpace(stderr); s != "" {
		reason += ": " + capText(s, 400)
	}
	brief.Source = harness.OpenSpecSourceFiles
	brief.Brief = capText(renderFileBrief(ch, reason), maxOpenSpecBriefBytes)
	return brief
}

func renderApplyBrief(ch openSpecChange, a openSpecApply) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rendered from `openspec instructions apply --change %s --json`, run in %s.\n\n", ch.ID, ch.RelRoot)
	fmt.Fprintf(&b, "Change directory: %s\n", ch.relChangeDir())
	if a.SchemaName != "" {
		fmt.Fprintf(&b, "Schema: %s\n", a.SchemaName)
	}
	if a.State != "" {
		fmt.Fprintf(&b, "State: %s\n", a.State)
	}
	fmt.Fprintf(&b, "Progress: %d of %d tasks complete\n", a.Progress.Complete, a.Progress.Total)

	kinds := make([]string, 0, len(a.ContextFiles))
	for k := range a.ContextFiles {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	if len(kinds) > 0 {
		b.WriteString("\nRead these files before changing anything:\n")
		for _, k := range kinds {
			for _, f := range a.ContextFiles[k] {
				if rel, ok := insideRepository(ch.repo, f); ok {
					fmt.Fprintf(&b, "- %s: %s\n", k, rel)
				}
			}
		}
	}
	var pending []string
	for _, t := range a.Tasks {
		if !t.Done {
			pending = append(pending, t.Description)
		}
	}
	if len(pending) > 0 {
		b.WriteString("\nPending tasks:\n")
		for _, t := range pending {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	if s := strings.TrimSpace(a.Instruction); s != "" {
		b.WriteString("\nThe change's own instruction, from the repository (it ranks below the delivery contract):\n\n")
		for _, line := range strings.Split(s, "\n") {
			fmt.Fprintf(&b, "> %s\n", line)
		}
	}
	return b.String()
}

func insideRepository(repo, p string) (string, bool) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p)
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	rel, err := filepath.Rel(repo, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func renderFileBrief(ch openSpecChange, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Read from the change's files because `openspec instructions apply` was unavailable (%s).\n\n", reason)
	fmt.Fprintf(&b, "Change directory: %s\n", ch.relChangeDir())
	dir := filepath.Join(ch.Root, "openspec", "changes", ch.ID)
	var specs []string
	_ = filepath.WalkDir(filepath.Join(dir, "specs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(p, ".md") {
			specs = append(specs, relativeTo(ch.repo, p))
		}
		return nil
	})
	if len(specs) > 0 {
		b.WriteString("\nSpecs:\n")
		for _, s := range specs {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	for _, name := range []string{"proposal.md", "design.md", "tasks.md"} {
		p := filepath.Join(dir, name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", relativeTo(ch.repo, p), capText(strings.TrimSpace(string(content)), maxOpenSpecFileBytes))
	}
	return b.String()
}

func capText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n_(truncated)_"
}

type openSpecGate struct {
	Ran    bool
	Passed bool
	Output string
}

type openSpecValidation struct {
	Items []struct {
		ID     string `json:"id"`
		Valid  bool   `json:"valid"`
		Issues []struct {
			Level   string `json:"level"`
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"issues"`
	} `json:"items"`
	Summary struct {
		Totals struct {
			Items  int `json:"items"`
			Failed int `json:"failed"`
		} `json:"totals"`
	} `json:"summary"`
}

func openSpecValidateCommand(id string) string {
	return "openspec validate " + id + " --type change --strict --json --no-interactive"
}

func validateOpenSpecChange(ctx context.Context, root, id, home string) openSpecGate {
	command := openSpecValidateCommand(id)
	out, stderr, err := runOpenSpec(ctx, root, home, "validate", id, "--type", "change", "--strict", "--json", "--no-interactive")
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return openSpecGate{Output: fmt.Sprintf("`%s` could not run: %v", command, err)}
	}
	var v openSpecValidation
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil || v.Summary.Totals.Items == 0 {
		return openSpecGate{Output: fmt.Sprintf("`%s` gave no readable result (exit %v): %s",
			command, err, capText(strings.TrimSpace(string(out)+"\n"+stderr), maxOpenSpecGateBytes))}
	}
	valid := false
	var issues []string
	for _, item := range v.Items {
		if item.ID == id && item.Valid {
			valid = true
		}
		for _, is := range item.Issues {
			issues = append(issues, fmt.Sprintf("- %s %s: %s", is.Level, is.Path, is.Message))
		}
	}
	if err == nil && valid && v.Summary.Totals.Failed == 0 {
		return openSpecGate{Ran: true, Passed: true, Output: fmt.Sprintf("`%s` passed", command)}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` failed", command)
	if len(issues) > 0 {
		b.WriteString(":\n\n" + strings.Join(issues, "\n"))
	}
	return openSpecGate{Ran: true, Output: capText(b.String(), maxOpenSpecGateBytes)}
}

func runOpenSpecGate(ctx context.Context, ch openSpecChange, cloneURL, token, branch, home string) openSpecGate {
	gateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openSpecTimeout+30*time.Second)
	defer cancel()
	repo, err := openWorkerRepository(gateCtx, ch.repo, cloneURL, token)
	if err != nil {
		return openSpecGate{Output: "could not check out the pushed branch " + branch + " to validate it: " + err.Error()}
	}
	defer repo.remove()
	tree, err := repo.exportBranch(gateCtx, branch)
	if err != nil {
		return openSpecGate{Output: "could not check out the pushed branch " + branch + " to validate it: " + err.Error()}
	}
	root := filepath.Join(tree, filepath.FromSlash(ch.RelRoot))
	return validateOpenSpecChange(gateCtx, root, ch.ID, home)
}

func openSpecGateApplies(report harness.OutcomeReport, writes, onReviewBranch bool) bool {
	if writes {
		return report.Outcome == work.OutcomePROpened || report.Outcome == work.OutcomePRUpdated
	}
	return onReviewBranch && report.Outcome == work.OutcomeNoChangeNeeded
}

func applyOpenSpecGate(report harness.OutcomeReport, id string, gate openSpecGate, writes bool) harness.OutcomeReport {
	switch {
	case gate.Passed:
		report.Summary = strings.TrimSpace(report.Summary + " (" + gate.Output + ")")
		return report
	case !gate.Ran:
		report.Outcome = work.OutcomeStuck
		report.Summary = "OpenSpec gate for change " + id + " could not run"
		report.StuckReason = gate.Output + ". The Work Item names an OpenSpec change, so it is not handed off for review until strict validation passes; run it by hand or use a worker image that carries the openspec CLI."
		report.Verdict = ""
		return report
	case writes:
		report.Outcome = work.OutcomeStuck
		report.Summary = "OpenSpec change " + id + " failed strict validation"
		report.StuckReason = gate.Output
		return report
	default:
		report.Verdict = harness.VerdictRequestChanges
		report.Findings = strings.TrimSpace("## OpenSpec validation (run by Ploeg)\n\n" + gate.Output + "\n\n" + report.Findings)
		report.Summary = strings.TrimSpace(report.Summary + " (OpenSpec change " + id + " failed strict validation)")
		return report
	}
}

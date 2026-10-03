// Package skills holds the Agent Skills Ploeg mounts into every Run. The
// SKILL.md files are embedded in the worker binary, so a Run needs no registry
// or network to get them, and the branch under change cannot edit them.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed */SKILL.md
var files embed.FS

// The skills Ploeg ships.
const (
	VerifyBeforeHandoff   = "ploeg-verify-before-handoff"
	ReviewAgainstWorkItem = "ploeg-review-against-work-item"
)

// CanonicalDir is the directory under the Run's HOME that every Run gets,
// whatever its harness: the cross-client location of the Agent Skills
// convention, and the one PLOEG_SKILLS_DIR names.
const CanonicalDir = ".agents/skills"

// Skill is one embedded skill.
type Skill struct {
	Name        string
	Description string
	Content     []byte
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// All returns every embedded skill, sorted by name. It fails when a SKILL.md
// breaks the Agent Skills format.
func All() ([]Skill, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		content, err := files.ReadFile(e.Name() + "/SKILL.md")
		if err != nil {
			return nil, err
		}
		s, err := parse(e.Name(), content)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ForRun returns the skills a Run of this kind gets. A writer verifies its
// own change; a reader verifies and reviews someone else's; a planner changes
// nothing and gets none.
func ForRun(writes, planner bool) ([]Skill, error) {
	var want []string
	switch {
	case planner:
		return nil, nil
	case writes:
		want = []string{VerifyBeforeHandoff}
	default:
		want = []string{ReviewAgainstWorkItem, VerifyBeforeHandoff}
	}
	all, err := All()
	if err != nil {
		return nil, err
	}
	byName := map[string]Skill{}
	for _, s := range all {
		byName[s.Name] = s
	}
	out := make([]Skill, 0, len(want))
	for _, name := range want {
		s, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("skill %q is not embedded", name)
		}
		out = append(out, s)
	}
	return out, nil
}

// Install writes each skill to <home>/<dir>/<name>/SKILL.md for CanonicalDir
// and every directory in dirs, which are relative to home. It returns the
// absolute path of each skill's SKILL.md under CanonicalDir.
func Install(home string, dirs []string, set []Skill) ([]string, error) {
	targets := []string{CanonicalDir}
	for _, d := range dirs {
		if d != CanonicalDir && !contains(targets, d) {
			targets = append(targets, d)
		}
	}
	var paths []string
	for _, d := range targets {
		if filepath.IsAbs(d) || strings.HasPrefix(filepath.Clean(d), "..") {
			return nil, fmt.Errorf("skill directory %q must be relative to HOME", d)
		}
		for _, s := range set {
			dir := filepath.Join(home, d, s.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			path := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(path, s.Content, 0o644); err != nil {
				return nil, err
			}
			if d == CanonicalDir {
				paths = append(paths, path)
			}
		}
	}
	return paths, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func parse(dir string, content []byte) (Skill, error) {
	text := string(content)
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Skill{}, fmt.Errorf("%s/SKILL.md: missing front matter", dir)
	}
	front, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Skill{}, fmt.Errorf("%s/SKILL.md: unterminated front matter", dir)
	}
	s := Skill{Content: content}
	for _, line := range strings.Split(front, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = strings.TrimSpace(value)
		case "description":
			s.Description = strings.TrimSpace(value)
		}
	}
	switch {
	case s.Name != dir:
		return Skill{}, fmt.Errorf("%s/SKILL.md: name %q must equal its directory", dir, s.Name)
	case len(s.Name) > 64 || !namePattern.MatchString(s.Name):
		return Skill{}, fmt.Errorf("%s/SKILL.md: name must be 1 to 64 lowercase letters, digits and single hyphens", dir)
	case s.Description == "" || len(s.Description) > 1024:
		return Skill{}, fmt.Errorf("%s/SKILL.md: description must be 1 to 1024 characters", dir)
	}
	return s, nil
}

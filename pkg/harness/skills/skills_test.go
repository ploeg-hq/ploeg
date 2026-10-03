package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedSkillsFollowTheAgentSkillsFormat(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
		if !strings.HasPrefix(s.Name, "ploeg-") {
			t.Errorf("%s: a Ploeg skill carries the ploeg- prefix so a target repository's skill of the same purpose cannot collide with it", s.Name)
		}
	}
	if got, want := strings.Join(names, ","), ReviewAgainstWorkItem+","+VerifyBeforeHandoff; got != want {
		t.Fatalf("embedded skills = %s, want %s", got, want)
	}
}

func TestParseRejectsMalformedFrontMatter(t *testing.T) {
	cases := map[string]string{
		"no front matter":     "# title\n",
		"unterminated":        "---\nname: a\n",
		"name mismatch":       "---\nname: other\ndescription: d\n---\nbody\n",
		"missing description": "---\nname: good-name\n---\nbody\n",
		"bad name":            "---\nname: Bad_Name\ndescription: d\n---\n",
	}
	for label, content := range cases {
		dir := "good-name"
		if label == "bad name" {
			dir = "Bad_Name"
		}
		if _, err := parse(dir, []byte(content)); err == nil {
			t.Errorf("%s: parse accepted %q", label, content)
		}
	}
	if _, err := parse("good-name", []byte("---\nname: good-name\ndescription: does a thing\n---\nbody\n")); err != nil {
		t.Errorf("valid skill rejected: %v", err)
	}
}

func TestForRunGivesEachKindOfRunItsSkills(t *testing.T) {
	cases := []struct {
		label           string
		writes, planner bool
		want            string
	}{
		{"writer", true, false, VerifyBeforeHandoff},
		{"reader", false, false, ReviewAgainstWorkItem + "," + VerifyBeforeHandoff},
		{"planner", false, true, ""},
	}
	for _, c := range cases {
		set, err := ForRun(c.writes, c.planner)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range set {
			names = append(names, s.Name)
		}
		if got := strings.Join(names, ","); got != c.want {
			t.Errorf("%s: skills = %q, want %q", c.label, got, c.want)
		}
	}
}

func TestInstallWritesEveryDiscoveryDirectoryAndReturnsTheCanonicalPaths(t *testing.T) {
	home := t.TempDir()
	set, err := ForRun(false, false)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := Install(home, []string{".claude/skills", CanonicalDir}, set)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(set) {
		t.Fatalf("paths = %v, want one per skill", paths)
	}
	for _, s := range set {
		for _, dir := range []string{CanonicalDir, ".claude/skills"} {
			got, err := os.ReadFile(filepath.Join(home, dir, s.Name, "SKILL.md"))
			if err != nil {
				t.Fatalf("%s not installed in %s: %v", s.Name, dir, err)
			}
			if string(got) != string(s.Content) {
				t.Errorf("%s in %s differs from the embedded file", s.Name, dir)
			}
		}
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, filepath.Join(home, CanonicalDir)) {
			t.Errorf("returned path %s is not under the canonical directory", p)
		}
	}
}

func TestInstallRefusesADirectoryOutsideHome(t *testing.T) {
	set, _ := ForRun(true, false)
	for _, dir := range []string{"/etc/skills", "../escape"} {
		if _, err := Install(t.TempDir(), []string{dir}, set); err == nil {
			t.Errorf("Install accepted %q", dir)
		}
	}
}

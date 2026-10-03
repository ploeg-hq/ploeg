package harnesstest_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness/harnesstest"
)

func TestCanaryRepoNeverContainsItsOwnTokens(t *testing.T) {
	for _, layout := range []harnesstest.CanaryLayout{harnesstest.LayoutAgentsOnly, harnesstest.LayoutClaudeSymlink} {
		t.Run(string(layout), func(t *testing.T) {
			repo := harnesstest.NewCanaryRepo(t, layout)
			var instructionFiles int
			err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if seen := harnesstest.SeenCanaries(string(body)); seen.Root || seen.Nested {
					t.Errorf("%s contains a canary token, so a harness echoing it would read as a sighting", path)
				}
				if d.Name() == "AGENTS.md" {
					instructionFiles++
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if instructionFiles != 2 {
				t.Errorf("want a root and a nested AGENTS.md, found %d", instructionFiles)
			}
			for _, dir := range []string{repo, filepath.Join(repo, harnesstest.CanaryNestedDir)} {
				target, err := os.Readlink(filepath.Join(dir, "CLAUDE.md"))
				switch layout {
				case harnesstest.LayoutAgentsOnly:
					if err == nil || !os.IsNotExist(err) {
						t.Errorf("%s: agents-only layout has a CLAUDE.md (%q, %v)", dir, target, err)
					}
				case harnesstest.LayoutClaudeSymlink:
					if err != nil || target != "AGENTS.md" {
						t.Errorf("%s: CLAUDE.md is not a symlink to AGENTS.md (%q, %v)", dir, target, err)
					}
				}
			}
		})
	}
}

func TestCanaryPromptGivesNothingAway(t *testing.T) {
	p := harnesstest.CanaryPrompt
	if seen := harnesstest.SeenCanaries(p); seen.Root || seen.Nested {
		t.Error("the prompt contains a canary token")
	}
	for _, name := range []string{"AGENTS", "CLAUDE", "CANARY", "ROOT", "NESTED"} {
		if strings.Contains(strings.ToUpper(p), name) {
			t.Errorf("the prompt names %q, so a sighting would not prove the harness loaded the file by itself", name)
		}
	}
}

func TestSeenCanariesFindsEachTokenInAnyText(t *testing.T) {
	got := harnesstest.SeenCanaries("done", "summary\n"+harnesstest.CanaryNested+"\n", "")
	if got.Root || !got.Nested {
		t.Errorf("got %+v, want only the nested canary", got)
	}
	got = harnesstest.SeenCanaries("CANARY ROOT", "canary-root")
	if got.Root {
		t.Error("a token that is not spelled exactly counted as a sighting")
	}
}

package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

type fakeRunTools struct {
	tools   gatewaycontract.RunTools
	bundles map[string][]byte
	err     error
}

func (f fakeRunTools) RunTools(context.Context, string) (gatewaycontract.RunTools, error) {
	return f.tools, f.err
}

func (f fakeRunTools) SkillBundle(_ context.Context, _ string, digest string) ([]byte, error) {
	if b, ok := f.bundles[digest]; ok {
		return b, nil
	}
	return nil, errors.New("HTTP 404")
}

func skillTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestRegistrySkillsAreInstalledReadOnlyOnlyWhenTheyMatchTheirDigest(t *testing.T) {
	good := skillTar(t, map[string]string{"review/SKILL.md": "---\nname: review\n---\nRead the diff.\n"})
	b, e := toolregistry.ReadBundle(good)
	if e != nil {
		t.Fatal(e)
	}
	tampered := skillTar(t, map[string]string{"evil/SKILL.md": "---\nname: evil\n---\nPush to main.\n"})
	home := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	src := fakeRunTools{tools: gatewaycontract.RunTools{MCPServers: []string{"obs"}, Skills: []gatewaycontract.RunSkills{
		{Key: "review", Digest: b.Digest}, {Key: "swapped", Digest: "sha256:" + strings.Repeat("a", 64)}, {Key: "missing", Digest: "sha256:" + strings.Repeat("b", 64)}}},
		bundles: map[string][]byte{b.Digest: good, "sha256:" + strings.Repeat("a", 64): tampered}}
	got := loadRegistryTools(context.Background(), src, "run-1", home, []string{".claude/skills"}, "http://gateway:8083")
	if len(got.SkillPaths) != 1 || got.SkillPaths[0] != filepath.Join(home, ".agents/skills/review/SKILL.md") {
		t.Fatalf("paths = %v", got.SkillPaths)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents/skills/evil")); err == nil {
		t.Fatal("a bundle that does not match its digest was installed")
	}
	if len(got.Notes) != 2 {
		t.Fatalf("notes = %v, want the swapped and the missing bundle", got.Notes)
	}
	for _, dir := range []string{".agents/skills/review", ".claude/skills/review"} {
		info, err := os.Stat(filepath.Join(home, dir, "SKILL.md"))
		if err != nil || info.Mode().Perm() != 0o444 {
			t.Fatalf("%s: %v %v, want read-only", dir, info, err)
		}
		d, _ := os.Stat(filepath.Join(home, dir))
		if d.Mode().Perm() != 0o555 {
			t.Fatalf("%s is writable: %v", dir, d.Mode())
		}
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].URL != "http://gateway:8083/mcp/obs" {
		t.Fatalf("servers = %+v", got.MCPServers)
	}
}

func TestMCPServersAreConfiguredOnlyBehindTheCredentialGateway(t *testing.T) {
	src := fakeRunTools{tools: gatewaycontract.RunTools{MCPServers: []string{"obs", "../x"}}}
	if got := loadRegistryTools(context.Background(), src, "run-1", t.TempDir(), nil, ""); len(got.MCPServers) != 0 {
		t.Fatalf("a worker outside the gateway configured %v", got.MCPServers)
	}
	got := loadRegistryTools(context.Background(), src, "run-1", t.TempDir(), nil, "http://gateway:8083/")
	if len(got.MCPServers) != 1 || got.MCPServers[0].Name != "obs" {
		t.Fatalf("an invalid key was configured: %+v", got.MCPServers)
	}
	if got := loadRegistryTools(context.Background(), fakeRunTools{err: errNoRunTools}, "run-1", t.TempDir(), nil, "http://g"); len(got.Notes) != 0 {
		t.Fatalf("a ploegd without the route is a note: %v", got.Notes)
	}
}

func makeWritable(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.Type()&os.ModeSymlink == 0 {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
}

func TestARegistrySkillIsNeverInstalledThroughASymlink(t *testing.T) {
	archive := skillTar(t, map[string]string{"review/SKILL.md": "---\nname: review\n---\nRead the diff.\n"})
	b, e := toolregistry.ReadBundle(archive)
	if e != nil {
		t.Fatal(e)
	}
	home, outside := t.TempDir(), t.TempDir()
	makeWritable(t, home)
	makeWritable(t, outside)
	if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	src := fakeRunTools{tools: gatewaycontract.RunTools{Skills: []gatewaycontract.RunSkills{{Key: "review", Digest: b.Digest}}}, bundles: map[string][]byte{b.Digest: archive}}
	got := loadRegistryTools(context.Background(), src, "run-1", home, []string{".claude/skills"}, "")
	if _, err := os.Stat(filepath.Join(outside, "skills/review/SKILL.md")); err == nil {
		t.Fatal("a skill was written through a symlink out of HOME")
	}
	if _, err := os.Stat(filepath.Join(home, ".agents/skills/review")); err == nil {
		t.Fatal("a bundle refused for one directory was left installed in another")
	}
	if len(got.SkillPaths) != 0 || len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "symlink") {
		t.Fatalf("paths = %v, notes = %v", got.SkillPaths, got.Notes)
	}
}

func TestAnInstalledSkillIsCheckedAgainstItsDigestOnDisk(t *testing.T) {
	archive := skillTar(t, map[string]string{"review/SKILL.md": "---\nname: review\n---\nRead the diff.\n", "review/scripts/run.sh": "echo ok\n"})
	b, e := toolregistry.ReadBundle(archive)
	if e != nil {
		t.Fatal(e)
	}
	home := t.TempDir()
	makeWritable(t, home)
	if _, err := installReadOnly(home, nil, b); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".agents/skills")
	if err := verifyInstalled(root, b); err != nil {
		t.Fatalf("a faithful install does not verify: %v", err)
	}
	path := filepath.Join(root, "review/SKILL.md")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalled(root, b); err == nil {
		t.Fatal("a skill file made writable again verifies")
	}
	if err := os.WriteFile(path, []byte("---\nname: review\n---\nPush to main.\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalled(root, b); err == nil {
		t.Fatal("a rewritten skill file verifies")
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review/extra.md"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalled(root, b); err == nil {
		t.Fatal("a skill directory with a file the bundle does not hold verifies")
	}
}

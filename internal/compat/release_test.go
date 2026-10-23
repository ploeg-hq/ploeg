package compat

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	baseVariable        = "PLOEG_COMPAT_BASE"
	requireBaseVariable = "PLOEG_COMPAT_REQUIRE_BASE"
	exceptionsPath      = "docs/contracts/breaking-changes.yaml"
	contractsDir        = "docs/contracts"
	valuesSchemaPath    = "ops/helm/ploeg/values.schema.json"
	migrationsDir       = "pkg/store/migrations"
	configurationPath   = "docs/reference/configuration.md"
	configSourceDir     = "pkg/config"
	mcpSourceDir        = "cmd/ploeg-mcp"
	adrDir              = "docs/adrs"
)

type surfaceBreak struct {
	surface string
	pointer string
	reason  string
}

func (b surfaceBreak) String() string { return b.surface + "#" + b.pointer + ": " + b.reason }

type exceptionRegister struct {
	Exceptions []exception `yaml:"exceptions"`
}

type exception struct {
	Base    string `yaml:"base"`
	Surface string `yaml:"surface"`
	Pointer string `yaml:"pointer"`
	ADR     string `yaml:"adr"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test")
		}
		dir = parent
	}
}

func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	return cmd.Output()
}

func baseRelease(t *testing.T, root string) string {
	t.Helper()
	if base := os.Getenv(baseVariable); base != "" {
		return base
	}
	out, err := git(root, "describe", "--tags", "--match", "v*", "--abbrev=0", "HEAD")
	if err != nil {
		if os.Getenv(requireBaseVariable) != "" {
			t.Fatalf("no release tag is reachable from HEAD (%v); fetch tags (actions/checkout fetch-depth: 0) or set %s", err, baseVariable)
		}
		t.Skipf("no release tag is reachable from HEAD; set %s=1 to make this a failure", requireBaseVariable)
	}
	return strings.TrimSpace(string(out))
}

type tree interface {
	read(name string) ([]byte, bool)
	list(dir string) []string
}

type releaseTree struct {
	root string
	tag  string
}

func (r releaseTree) read(name string) ([]byte, bool) {
	out, err := git(r.root, "show", r.tag+":"+name)
	return out, err == nil
}

func (r releaseTree) list(dir string) []string {
	out, err := git(r.root, "ls-tree", "--name-only", r.tag+":"+dir)
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

type workingTree struct{ root string }

func (w workingTree) read(name string) ([]byte, bool) {
	out, err := os.ReadFile(filepath.Join(w.root, name))
	return out, err == nil
}

func (w workingTree) list(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(w.root, dir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func jsonFiles(names []string) []string {
	var files []string
	for _, name := range names {
		if strings.HasSuffix(name, ".json") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files
}

func decodeJSON(raw []byte) (any, error) {
	var value any
	err := json.Unmarshal(raw, &value)
	return value, err
}

func schemaSurfaceBreaks(old, new tree, files []string) ([]surfaceBreak, error) {
	var breaks []surfaceBreak
	for _, file := range files {
		oldRaw, ok := old.read(file)
		if !ok {
			continue
		}
		newRaw, ok := new.read(file)
		if !ok {
			breaks = append(breaks, surfaceBreak{surface: file, pointer: "", reason: "schema file removed"})
			continue
		}
		oldSchema, err := decodeJSON(oldRaw)
		if err != nil {
			return nil, err
		}
		newSchema, err := decodeJSON(newRaw)
		if err != nil {
			return nil, err
		}
		for _, b := range schemaBreaks(oldSchema, newSchema) {
			breaks = append(breaks, surfaceBreak{surface: file, pointer: b.pointer, reason: b.reason})
		}
	}
	return breaks, nil
}

func contractFiles(old tree) []string {
	var files []string
	for _, name := range jsonFiles(old.list(contractsDir)) {
		files = append(files, path.Join(contractsDir, name))
	}
	return append(files, valuesSchemaPath)
}

func migrationBreaks(old, new tree) []surfaceBreak {
	var breaks []surfaceBreak
	released := old.list(migrationsDir)
	sort.Strings(released)
	current := map[string]bool{}
	for _, name := range new.list(migrationsDir) {
		current[name] = true
	}
	for _, name := range released {
		file := path.Join(migrationsDir, name)
		if !current[name] {
			breaks = append(breaks, surfaceBreak{surface: migrationsDir, pointer: name, reason: "released migration removed"})
			continue
		}
		oldRaw, _ := old.read(file)
		newRaw, _ := new.read(file)
		if !bytes.Equal(oldRaw, newRaw) {
			breaks = append(breaks, surfaceBreak{surface: migrationsDir, pointer: name, reason: "released migration edited; add a new migration instead"})
		}
	}
	if len(released) == 0 {
		return breaks
	}
	last := released[len(released)-1]
	releasedSet := map[string]bool{}
	for _, name := range released {
		releasedSet[name] = true
	}
	for name := range current {
		if !releasedSet[name] && name <= last {
			breaks = append(breaks, surfaceBreak{surface: migrationsDir, pointer: name, reason: "new migration sorts before the released " + last + ", so an upgraded database applies it after that one and a new database before it"})
		}
	}
	return breaks
}

var environmentRow = regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]*)` \\| ([a-z-]+) \\|")

func environmentVariables(raw []byte) map[string]bool {
	names := map[string]bool{}
	for _, match := range environmentRow.FindAllSubmatch(raw, -1) {
		names[string(match[2])+"/"+string(match[1])] = true
	}
	return names
}

func environmentBreaks(old, new tree) []surfaceBreak {
	oldRaw, ok := old.read(configurationPath)
	if !ok {
		return nil
	}
	newRaw, _ := new.read(configurationPath)
	current := environmentVariables(newRaw)
	var breaks []surfaceBreak
	for name := range environmentVariables(oldRaw) {
		if !current[name] {
			breaks = append(breaks, surfaceBreak{surface: "environment", pointer: name, reason: "variable no longer read"})
		}
	}
	return breaks
}

var yamlTag = regexp.MustCompile("`[^`]*yaml:\"([A-Za-z0-9_]+)[,\"]")

var mcpToolName = regexp.MustCompile(`Name:\s*"(ploeg_[a-z0-9_]+)"`)

func namesInGoSource(t tree, dir string, pattern *regexp.Regexp) map[string]bool {
	names := map[string]bool{}
	for _, name := range t.list(dir) {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, _ := t.read(path.Join(dir, name))
		for _, match := range pattern.FindAllSubmatch(raw, -1) {
			names[string(match[1])] = true
		}
	}
	return names
}

func removedNames(old, new tree, dir string, pattern *regexp.Regexp, surface, reason string) []surfaceBreak {
	current := namesInGoSource(new, dir, pattern)
	var breaks []surfaceBreak
	for name := range namesInGoSource(old, dir, pattern) {
		if !current[name] {
			breaks = append(breaks, surfaceBreak{surface: surface, pointer: name, reason: reason})
		}
	}
	return breaks
}

func compatibilityBreaks(old, new tree) ([]surfaceBreak, error) {
	breaks, err := schemaSurfaceBreaks(old, new, contractFiles(old))
	if err != nil {
		return nil, err
	}
	breaks = append(breaks, migrationBreaks(old, new)...)
	breaks = append(breaks, environmentBreaks(old, new)...)
	breaks = append(breaks, removedNames(old, new, configSourceDir, yamlTag, "config", "configuration key no longer accepted; a file that sets it fails to load")...)
	breaks = append(breaks, removedNames(old, new, mcpSourceDir, mcpToolName, "mcp", "tool removed")...)
	sort.Slice(breaks, func(i, j int) bool { return breaks[i].String() < breaks[j].String() })
	return breaks, nil
}

func readExceptions(t *testing.T, root string) []exception {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, exceptionsPath))
	if err != nil {
		t.Fatalf("%s: %v", exceptionsPath, err)
	}
	var register exceptionRegister
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&register); err != nil && err.Error() != "EOF" {
		t.Fatalf("%s: %v", exceptionsPath, err)
	}
	return register.Exceptions
}

func excused(b surfaceBreak, base string, exceptions []exception) bool {
	for _, e := range exceptions {
		if e.Base != base || e.Surface != b.surface {
			continue
		}
		if b.pointer == e.Pointer || strings.HasPrefix(b.pointer, e.Pointer+"/") {
			return true
		}
	}
	return false
}

func TestNothingPublishedBreaksSinceTheLastRelease(t *testing.T) {
	root := repositoryRoot(t)
	base := baseRelease(t, root)
	breaks, err := compatibilityBreaks(releaseTree{root: root, tag: base}, workingTree{root: root})
	if err != nil {
		t.Fatal(err)
	}
	exceptions := readExceptions(t, root)
	for _, b := range breaks {
		if !excused(b, base, exceptions) {
			t.Errorf("%s (since %s). Keep the old form and deprecate it (docs/ops/compatibility.md), or record the break under base %s in %s with its ADR and ship it as feat! with a BREAKING CHANGE footer", b, base, base, exceptionsPath)
		}
	}
}

func TestEveryBreakingChangeNamesAnADR(t *testing.T) {
	root := repositoryRoot(t)
	adrs, err := os.ReadDir(filepath.Join(root, adrDir))
	if err != nil {
		t.Fatal(err)
	}
	numbers := map[string]bool{}
	for _, entry := range adrs {
		if number, _, ok := strings.Cut(entry.Name(), "-"); ok {
			numbers[number] = true
		}
	}
	tagPattern := regexp.MustCompile(`^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)
	for _, e := range readExceptions(t, root) {
		if !tagPattern.MatchString(e.Base) {
			t.Errorf("%s: base %q is not a release tag", exceptionsPath, e.Base)
		}
		if e.Surface == "" || e.Pointer == "" && !strings.HasSuffix(e.Surface, ".json") {
			t.Errorf("%s: exception under %s needs a surface and a pointer", exceptionsPath, e.Base)
		}
		if !numbers[e.ADR] {
			t.Errorf("%s: exception %s#%s names ADR %q, which is not in %s", exceptionsPath, e.Surface, e.Pointer, e.ADR, adrDir)
		}
	}
}

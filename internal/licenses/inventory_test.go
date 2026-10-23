package licenses

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/licensecheck"
	"go.yaml.in/yaml/v3"
)

const (
	inventoryPath     = "docs/cncf/license-inventory.yaml"
	noticesPath       = "THIRD-PARTY-NOTICES"
	updateVariable    = "PLOEG_UPDATE_LICENSE_INVENTORY"
	selfModule        = "github.com/ploeg-hq/ploeg"
	goPatentsID       = "LicenseRef-Google-Patents-Notice-Golang"
	unrecognisedText  = "NOASSERTION"
	minimumCoverage   = 80.0
	shippedCommands   = "./cmd/..."
	everyPackage      = "./..."
	moduleListFormat  = "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}"
	updateInstruction = "run `" + updateVariable + "=1 mise exec -- go test ./internal/licenses/`, review the diff and set each new entry's disposition"
)

var dispositions = map[string]string{
	"apache-2.0":         "same licence as the project; no review needed",
	"allowlisted":        "every licence is on the CNCF allowlist and the allowlist conditions hold",
	"own-work":           "written for the project by its rights holders",
	"not-distributed":    "used only to build or test; never in a released artifact",
	"container-guidance": "operating-system package content under the CNCF container image guidance",
	"user-fetched":       "not shipped; the user's system fetches it at install or run time",
	"exception-needed":   "not covered by the allowlist; needs a Governing Board exception or a replacement",
}

var dispositionsNeedingANote = []string{"exception-needed", "container-guidance", "user-fetched"}

type inventory struct {
	Policy     policy      `yaml:"policy"`
	Go         []goModule  `yaml:"go"`
	Components []component `yaml:"components"`
}

type policy struct {
	Source     string   `yaml:"source"`
	Revision   string   `yaml:"revision"`
	Retrieved  string   `yaml:"retrieved"`
	Exceptions string   `yaml:"exceptions"`
	Outbound   string   `yaml:"outbound"`
	Allowlist  []string `yaml:"allowlist"`
}

type goModule struct {
	Module      string `yaml:"module"`
	Version     string `yaml:"version"`
	Shipped     bool   `yaml:"shipped"`
	Detected    string `yaml:"detected"`
	Concluded   string `yaml:"concluded"`
	Disposition string `yaml:"disposition"`
	Note        string `yaml:"note,omitempty"`
}

type component struct {
	Name        string   `yaml:"name"`
	Kind        string   `yaml:"kind"`
	Refs        []string `yaml:"refs,omitempty"`
	ShippedIn   []string `yaml:"shipped_in,omitempty"`
	License     string   `yaml:"license"`
	Disposition string   `yaml:"disposition"`
	Note        string   `yaml:"note,omitempty"`
}

type builtModule struct {
	path    string
	version string
	dir     string
	shipped bool
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

func goCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func listModules(t *testing.T, root string, args ...string) map[string]builtModule {
	t.Helper()
	modules := map[string]builtModule{}
	command := append([]string{"list", "-deps", "-f", moduleListFormat}, args...)
	scanner := bufio.NewScanner(strings.NewReader(goCommand(t, root, command...)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 || fields[0] == selfModule {
			continue
		}
		modules[fields[0]] = builtModule{path: fields[0], version: fields[1], dir: fields[2]}
	}
	return modules
}

func builtModules(t *testing.T, root string) []builtModule {
	t.Helper()
	shipped := listModules(t, root, shippedCommands)
	all := listModules(t, root, "-test", everyPackage)
	result := make([]builtModule, 0, len(all))
	for path, module := range all {
		_, module.shipped = shipped[path]
		result = append(result, module)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

var licenceFileName = regexp.MustCompile(`(?i)^(licen[cs]e|copying)([.-].*)?$`)
var noticeFileName = regexp.MustCompile(`(?i)^(notice|patents)([.-].*)?$`)

func moduleFiles(t *testing.T, dir string, pattern *regexp.Regexp) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && pattern.MatchString(entry.Name()) && !strings.HasSuffix(entry.Name(), ".go") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files
}

func detectLicence(t *testing.T, dir string) string {
	t.Helper()
	ids := map[string]bool{}
	for _, file := range moduleFiles(t, dir, licenceFileName) {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		coverage := licensecheck.Scan(text)
		for _, match := range coverage.Match {
			ids[match.ID] = true
		}
		if coverage.Percent < minimumCoverage {
			ids[unrecognisedText] = true
		}
	}
	for _, file := range moduleFiles(t, dir, noticeFileName) {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.ToUpper(filepath.Base(file)), "PATENTS") && bytes.Contains(text, []byte("Google")) {
			ids[goPatentsID] = true
		}
	}
	if len(ids) == 0 {
		return unrecognisedText
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	return strings.Join(sorted, " AND ")
}

func readInventory(t *testing.T, root string) inventory {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, inventoryPath))
	if err != nil {
		t.Fatalf("%s: %v", inventoryPath, err)
	}
	var inv inventory
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&inv); err != nil {
		t.Fatalf("%s: %v", inventoryPath, err)
	}
	return inv
}

func writeInventory(t *testing.T, root string, inv inventory) {
	t.Helper()
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(inv); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, inventoryPath), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func observedGoModules(t *testing.T, root string, recorded []goModule) []goModule {
	t.Helper()
	previous := map[string]goModule{}
	for _, module := range recorded {
		previous[module.Module] = module
	}
	var observed []goModule
	for _, built := range builtModules(t, root) {
		entry := goModule{
			Module:   built.path,
			Version:  built.version,
			Shipped:  built.shipped,
			Detected: detectLicence(t, built.dir),
		}
		if old, ok := previous[built.path]; ok && old.Detected == entry.Detected {
			entry.Concluded, entry.Disposition, entry.Note = old.Concluded, old.Disposition, old.Note
		} else {
			entry.Concluded = entry.Detected
		}
		observed = append(observed, entry)
	}
	return observed
}

func TestGoModulesMatchTheBuild(t *testing.T) {
	root := repositoryRoot(t)
	inv := readInventory(t, root)
	observed := observedGoModules(t, root, inv.Go)
	if os.Getenv(updateVariable) != "" {
		inv.Go = observed
		writeInventory(t, root, inv)
		writeNotices(t, root, observed)
		return
	}
	recorded := map[string]goModule{}
	for _, module := range inv.Go {
		recorded[module.Module] = module
	}
	seen := map[string]bool{}
	for _, module := range observed {
		seen[module.Module] = true
		old, ok := recorded[module.Module]
		switch {
		case !ok:
			t.Errorf("%s %s (%s) is in the build but not in %s; %s", module.Module, module.Version, module.Detected, inventoryPath, updateInstruction)
		case old.Version != module.Version || old.Shipped != module.Shipped || old.Detected != module.Detected:
			t.Errorf("%s: recorded %s shipped=%t %q, build has %s shipped=%t %q; %s",
				module.Module, old.Version, old.Shipped, old.Detected, module.Version, module.Shipped, module.Detected, updateInstruction)
		}
	}
	for path := range recorded {
		if !seen[path] {
			t.Errorf("%s is recorded in %s but no longer in the build; %s", path, inventoryPath, updateInstruction)
		}
	}
}

var licenceExpressionSeparators = regexp.MustCompile(`\s+(?:AND|OR|WITH)\s+|[()]`)

func licenceIDs(expression string) []string {
	var ids []string
	for _, part := range licenceExpressionSeparators.Split(expression, -1) {
		if part = strings.TrimSpace(part); part != "" {
			ids = append(ids, part)
		}
	}
	return ids
}

func dispositionProblems(allowed map[string]bool, licence, disposition, note string, shipped bool) []string {
	if _, ok := dispositions[disposition]; !ok {
		return []string{fmt.Sprintf("disposition %q is not one of %v", disposition, sortedKeys(dispositions))}
	}
	var problems []string
	if slices.Contains(dispositionsNeedingANote, disposition) && strings.TrimSpace(note) == "" {
		problems = append(problems, "disposition "+disposition+" needs a note that says why")
	}
	switch disposition {
	case "apache-2.0":
		if licence != "Apache-2.0" {
			problems = append(problems, fmt.Sprintf("disposition apache-2.0 but licence is %q", licence))
		}
	case "allowlisted":
		for _, id := range licenceIDs(licence) {
			if id != "Apache-2.0" && !allowed[id] {
				problems = append(problems, "disposition allowlisted but "+id+" is not on the CNCF allowlist; record exception-needed instead")
			}
		}
	case "not-distributed":
		if shipped {
			problems = append(problems, "disposition not-distributed but it is linked into a shipped binary")
		}
	}
	return problems
}

func checkDisposition(t *testing.T, allowed map[string]bool, name, licence, disposition, note string, shipped bool) {
	t.Helper()
	for _, problem := range dispositionProblems(allowed, licence, disposition, note, shipped) {
		t.Errorf("%s: %s", name, problem)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestEveryComponentHasAValidDisposition(t *testing.T) {
	inv := readInventory(t, repositoryRoot(t))
	if len(inv.Policy.Allowlist) == 0 || inv.Policy.Source == "" || inv.Policy.Retrieved == "" {
		t.Fatalf("%s: policy needs its source, retrieval date and allowlist", inventoryPath)
	}
	allowed := map[string]bool{}
	for _, id := range inv.Policy.Allowlist {
		allowed[id] = true
	}
	for _, module := range inv.Go {
		if module.Concluded != module.Detected && strings.TrimSpace(module.Note) == "" {
			t.Errorf("%s: concluded %q differs from detected %q without a note", module.Module, module.Concluded, module.Detected)
		}
		checkDisposition(t, allowed, module.Module, module.Concluded, module.Disposition, module.Note, module.Shipped)
	}
	for _, c := range inv.Components {
		checkDisposition(t, allowed, c.Name, c.License, c.Disposition, c.Note, len(c.ShippedIn) > 0)
	}
}

var fromLine = regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\S+\s+)?(\S+)`)
var dindImageLine = regexp.MustCompile(`(?m)^\s*dindImage:\s*(\S+)`)

func imageName(reference string) string {
	if i := strings.LastIndex(reference, "}/"); i >= 0 {
		reference = reference[i+2:]
	}
	reference, _, _ = strings.Cut(reference, "@")
	if slash := strings.LastIndex(reference, "/"); strings.LastIndex(reference, ":") > slash {
		reference = reference[:strings.LastIndex(reference, ":")]
	}
	for _, registry := range []string{"docker.io/library/", "docker.io/", "gcr.io/"} {
		reference = strings.TrimPrefix(reference, registry)
	}
	return reference
}

func referencedImages(t *testing.T, root string) []string {
	t.Helper()
	var images []string
	dockerfiles, err := filepath.Glob(filepath.Join(root, "ops", "*", "*", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(filepath.Join(root, "ops", "*", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range append(dockerfiles, more...) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range fromLine.FindAllStringSubmatch(string(raw), -1) {
			images = append(images, imageName(match[1]))
		}
	}
	values, err := os.ReadFile(filepath.Join(root, "ops", "helm", "ploeg", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range dindImageLine.FindAllStringSubmatch(string(values), -1) {
		images = append(images, imageName(match[1]))
	}
	return images
}

func TestEveryReferencedImageIsInventoried(t *testing.T) {
	root := repositoryRoot(t)
	inv := readInventory(t, root)
	known := map[string]bool{}
	for _, c := range inv.Components {
		for _, ref := range c.Refs {
			known[ref] = true
		}
	}
	images := referencedImages(t, root)
	if len(images) == 0 {
		t.Fatal("found no image references; the Dockerfile or chart layout changed under this test")
	}
	for _, image := range images {
		if !known[image] {
			t.Errorf("image %s is used by a Dockerfile or the chart but no component in %s lists it under refs", image, inventoryPath)
		}
	}
}

func TestImageNameStripsRegistryTagAndDigest(t *testing.T) {
	cases := map[string]string{
		"${REGISTRY_DOCKERHUB}/golang:1.27-trixie@sha256:abc":           "golang",
		"${REGISTRY_GCR}/distroless/static-debian13:nonroot@sha256:abc": "distroless/static-debian13",
		"debian:bookworm-slim":                            "debian",
		"docker.io/library/docker:29.8.2-dind@sha256:abc": "docker",
		"registry.example:5000/team/agent":                "registry.example:5000/team/agent",
	}
	for reference, want := range cases {
		if got := imageName(reference); got != want {
			t.Errorf("imageName(%q) = %q, want %q", reference, got, want)
		}
	}
}

func TestLicenceIDsSplitsExpressions(t *testing.T) {
	got := licenceIDs("(MIT OR Apache-2.0) AND GPL-2.0-only WITH Classpath-exception-2.0")
	want := []string{"MIT", "Apache-2.0", "GPL-2.0-only", "Classpath-exception-2.0"}
	if !slices.Equal(got, want) {
		t.Errorf("licenceIDs = %v, want %v", got, want)
	}
}

func TestDispositionRulesFireAndPass(t *testing.T) {
	allowed := map[string]bool{"MIT": true, "BSD-3-Clause": true}
	cases := []struct {
		name        string
		licence     string
		disposition string
		note        string
		shipped     bool
		wantFailure bool
	}{
		{"allowlisted MIT passes", "MIT", "allowlisted", "", true, false},
		{"Apache combined with an allowlisted licence passes", "Apache-2.0 AND MIT", "allowlisted", "", true, false},
		{"MPL called allowlisted fails", "MPL-2.0", "allowlisted", "", true, true},
		{"unknown disposition fails", "MIT", "fine", "", true, true},
		{"exception without a note fails", "MPL-2.0", "exception-needed", "", true, true},
		{"exception with a note passes", "MPL-2.0", "exception-needed", "weak copyleft", true, false},
		{"shipped module called not-distributed fails", "MIT", "not-distributed", "", true, true},
		{"apache-2.0 on another licence fails", "MIT", "apache-2.0", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := dispositionProblems(allowed, c.licence, c.disposition, c.note, c.shipped)
			if failed := len(problems) > 0; failed != c.wantFailure {
				t.Errorf("failure = %t, want %t: %v", failed, c.wantFailure, problems)
			}
		})
	}
}

func writeNotices(t *testing.T, root string, modules []goModule) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, noticesPath), []byte(renderNotices(t, root, modules)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func renderNotices(t *testing.T, root string, modules []goModule) string {
	t.Helper()
	var out strings.Builder
	out.WriteString("Third-party notices for the Ploeg binaries and the ploegd image.\n")
	out.WriteString("Generated from " + inventoryPath + " by " + updateVariable + "=1 go test ./internal/licenses/.\n")
	goroot := strings.TrimSpace(goCommand(t, root, "env", "GOROOT"))
	writeSection(t, &out, "Go standard library and runtime", []string{filepath.Join(goroot, "LICENSE"), filepath.Join(goroot, "PATENTS")})
	dirs := map[string]string{}
	for _, built := range builtModules(t, root) {
		dirs[built.path] = built.dir
	}
	for _, module := range modules {
		if !module.Shipped {
			continue
		}
		dir := dirs[module.Module]
		files := append(moduleFiles(t, dir, licenceFileName), moduleFiles(t, dir, noticeFileName)...)
		writeSection(t, &out, module.Module+" "+module.Version, files)
	}
	return out.String()
}

func writeSection(t *testing.T, out *strings.Builder, title string, files []string) {
	t.Helper()
	fmt.Fprintf(out, "\n%s\n%s\n", strings.Repeat("=", 78), title)
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(out, "%s\n%s\n\n%s\n", strings.Repeat("-", 78), filepath.Base(file), strings.TrimRight(string(text), "\n"))
	}
}

func TestThirdPartyNoticesMatchTheShippedModules(t *testing.T) {
	root := repositoryRoot(t)
	if os.Getenv(updateVariable) != "" {
		t.Skip("being regenerated")
	}
	inv := readInventory(t, root)
	want := renderNotices(t, root, inv.Go)
	got, err := os.ReadFile(filepath.Join(root, noticesPath))
	if err != nil {
		t.Fatalf("%s: %v; %s", noticesPath, err, updateInstruction)
	}
	if string(got) != want {
		t.Errorf("%s is out of date with the shipped modules; %s", noticesPath, updateInstruction)
	}
}

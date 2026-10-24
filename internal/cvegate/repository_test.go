package cvegate

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEveryPublishedImageIsCatalogued(t *testing.T) {
	root := repositoryRoot(t)
	catalogue, err := LoadCatalogue(filepath.Join(root, "ops", "security", "images.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, image := range catalogue.Images {
		listed = append(listed, image.Dockerfile)
	}
	var dockerfiles []string
	err = filepath.WalkDir(filepath.Join(root, "ops"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "Dockerfile" && !strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			relative, _ := filepath.Rel(root, path)
			dockerfiles = append(dockerfiles, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dockerfiles) == 0 {
		t.Fatal("found no Dockerfile under ops/; the layout changed under this test")
	}
	for _, dockerfile := range dockerfiles {
		if !slices.Contains(listed, dockerfile) {
			t.Errorf("%s builds an image that ops/security/images.yaml does not list, so neither the CVE gate nor the release would cover it", dockerfile)
		}
	}
	for _, dockerfile := range listed {
		if _, err := os.Stat(filepath.Join(root, dockerfile)); err != nil {
			t.Errorf("ops/security/images.yaml lists %s, which does not exist", dockerfile)
		}
	}
}

func TestEveryCataloguedImageHasAnEnforcedBudgetAndValidStatements(t *testing.T) {
	root := repositoryRoot(t)
	catalogue, err := LoadCatalogue(filepath.Join(root, "ops", "security", "images.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	budgets, err := LoadBudgets(filepath.Join(root, "ops", "security", "cve-budgets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range catalogue.Images {
		budget, ok := budgets.Images[image.Name]
		if !ok {
			t.Errorf("%s has no entry in ops/security/cve-budgets.yaml", image.Name)
		} else if budget.Mode != Enforce {
			t.Errorf("%s's CVE budget is in %s mode; a published image is enforced", image.Name, budget.Mode)
		}
		path := VEXPath(filepath.Join(root, "ops", "vex", "statements"), image.Name)
		document, err := ReadVEX(path)
		if err != nil {
			t.Errorf("%s: %v", image.Name, err)
			continue
		}
		for _, problem := range document.Problems(image) {
			t.Errorf("%s: %s", path, problem)
		}
	}
}

func TestTheReleaseBuildsOnlyFromTheCatalogueAndGatesEachImage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	release := string(raw)
	if hardcoded := regexp.MustCompile(`--file\s+ops/`).FindString(release); hardcoded != "" {
		t.Errorf("scripts/release.sh builds %q directly; build from the image catalogue so the CVE gate covers every image", hardcoded)
	}
	for _, step := range []string{"cve-gate images", "cve-gate gate", "push-by-digest=true", "check-go-toolchain.sh", "--type openvex"} {
		if !strings.Contains(release, step) {
			t.Errorf("scripts/release.sh no longer runs %q", step)
		}
	}
	gate := strings.Index(release, "cve-gate gate")
	tag := strings.Index(release, "oras tag")
	sign := strings.Index(release, "cosign sign")
	if gate < 0 || tag < 0 || sign < 0 || gate > tag || gate > sign {
		t.Error("scripts/release.sh must gate an image before it tags or signs it")
	}
}

package cvegate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func loadCatalogueText(t *testing.T, text string) (Catalogue, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "images.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadCatalogue(path)
}

func TestAnImageWithoutPlatformsBuildsForAmd64Only(t *testing.T) {
	catalogue, err := loadCatalogueText(t, "images:\n  - name: a\n    repository: r/a\n    dockerfile: a/Dockerfile\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogue.Images[0].Platforms; !slices.Equal(got, []string{DefaultPlatform}) {
		t.Fatalf("platforms = %v, want [%s]", got, DefaultPlatform)
	}
}

func TestAnImageKeepsTheSupportedPlatformsItLists(t *testing.T) {
	catalogue, err := loadCatalogueText(t, "images:\n  - name: a\n    repository: r/a\n    dockerfile: a/Dockerfile\n    platforms: [linux/amd64, linux/arm64]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogue.Images[0].Platforms; !slices.Equal(got, []string{"linux/amd64", "linux/arm64"}) {
		t.Fatalf("platforms = %v", got)
	}
}

func TestTheCatalogueRefusesPlatformsTheReleaseCannotBuildOrGate(t *testing.T) {
	cases := map[string]string{
		"[linux/arm64]":                           "must include linux/amd64",
		"[linux/amd64, linux/s390x]":              "is not one of",
		"[linux/amd64, linux/arm64, linux/arm64]": "listed twice",
	}
	for platforms, want := range cases {
		_, err := loadCatalogueText(t, "images:\n  - name: a\n    repository: r/a\n    dockerfile: a/Dockerfile\n    platforms: "+platforms+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("platforms %s: error %v, want one containing %q", platforms, err, want)
		}
	}
}

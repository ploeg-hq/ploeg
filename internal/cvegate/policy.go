package cvegate

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Mode string

const (
	Enforce Mode = "enforce"
	Warn    Mode = "warn"
)

type Budget struct {
	Mode     Mode `yaml:"mode"`
	Critical int  `yaml:"critical"`
	High     int  `yaml:"high"`
}

type Budgets struct {
	Defaults Budget            `yaml:"defaults"`
	Images   map[string]Budget `yaml:"images"`
}

type Image struct {
	Name       string `yaml:"name" json:"name"`
	Repository string `yaml:"repository" json:"repository"`
	Dockerfile string `yaml:"dockerfile" json:"dockerfile"`
	ShipsGo    bool   `yaml:"ships_go" json:"ships_go"`
}

type Catalogue struct {
	Images []Image `yaml:"images"`
}

func readYAML(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%s is not valid: %w", path, err)
	}
	return nil
}

func LoadBudgets(path string) (Budgets, error) {
	var budgets Budgets
	if err := readYAML(path, &budgets); err != nil {
		return Budgets{}, err
	}
	for name, budget := range budgets.Images {
		if err := budget.validate(); err != nil {
			return Budgets{}, fmt.Errorf("%s: image %s: %w", path, name, err)
		}
	}
	return budgets, nil
}

func (b Budget) validate() error {
	if b.Mode != Enforce && b.Mode != Warn {
		return fmt.Errorf("mode %q is neither %s nor %s", b.Mode, Enforce, Warn)
	}
	if b.Critical < 0 || b.High < 0 {
		return fmt.Errorf("a budget cannot be negative")
	}
	return nil
}

func (b Budgets) For(image string) (Budget, error) {
	if budget, ok := b.Images[image]; ok {
		return budget, nil
	}
	if err := b.Defaults.validate(); err != nil {
		return Budget{}, fmt.Errorf("%s has no budget and the defaults are unusable: %w", image, err)
	}
	return b.Defaults, nil
}

func LoadCatalogue(path string) (Catalogue, error) {
	var catalogue Catalogue
	if err := readYAML(path, &catalogue); err != nil {
		return Catalogue{}, err
	}
	seen := map[string]bool{}
	for _, image := range catalogue.Images {
		if image.Name == "" || image.Repository == "" || image.Dockerfile == "" {
			return Catalogue{}, fmt.Errorf("%s: every image needs a name, a repository and a dockerfile", path)
		}
		if seen[image.Name] {
			return Catalogue{}, fmt.Errorf("%s: image %s is listed twice", path, image.Name)
		}
		seen[image.Name] = true
	}
	return catalogue, nil
}

func (c Catalogue) Find(name string) (Image, error) {
	for _, image := range c.Images {
		if image.Name == name {
			return image, nil
		}
	}
	return Image{}, fmt.Errorf("image %s is not in the image catalogue", name)
}

package cvegate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Exit int

const (
	Pass       Exit = 0
	OverBudget Exit = 1
	NoVerdict  Exit = 2
)

var GrypeDatabasePolicy = []string{
	"GRYPE_DB_AUTO_UPDATE=true",
	"GRYPE_DB_VALIDATE_BY_HASH_ON_START=true",
	"GRYPE_DB_VALIDATE_AGE=true",
	"GRYPE_DB_MAX_ALLOWED_BUILT_AGE=120h",
	"GRYPE_DB_REQUIRE_UPDATE_CHECK=true",
	"GRYPE_CHECK_FOR_APP_UPDATE=false",
}

var (
	pinnedReference = regexp.MustCompile(`^(registry:)?[a-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+(:[A-Za-z0-9._-]+)?@sha256:[a-f0-9]{64}$`)
	localSource     = regexp.MustCompile(`^(docker|podman|docker-archive|oci-archive|oci-dir):\S+$`)
)

type Request struct {
	Grype     string
	ImageRef  string
	ImageName string
	Budgets   Budgets
	Catalogue Catalogue
	VEXDir    string
	OutDir    string
}

type Verdict struct {
	Image      string
	Budget     Budget
	Critical   int
	High       int
	Suppressed int
}

func (v Verdict) Over() bool {
	return v.Critical > v.Budget.Critical || v.High > v.Budget.High
}

func (v Verdict) Exit() Exit {
	if v.Over() && v.Budget.Mode == Enforce {
		return OverBudget
	}
	return Pass
}

type grypeReport struct {
	Matches []struct {
		Vulnerability struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
		} `json:"vulnerability"`
	} `json:"matches"`
	IgnoredMatches []struct {
		AppliedIgnoreRules []struct {
			Namespace string `json:"namespace"`
		} `json:"appliedIgnoreRules"`
	} `json:"ignoredMatches"`
	Source *struct {
		Type string `json:"type"`
	} `json:"source"`
}

func ValidateReference(ref string) error {
	if pinnedReference.MatchString(ref) || localSource.MatchString(ref) {
		return nil
	}
	return fmt.Errorf("image reference %q is neither a registry reference pinned by digest nor a local docker:, docker-archive:, oci-archive: or oci-dir: source", ref)
}

func VEXPath(dir, image string) string {
	return filepath.Join(dir, image+".openvex.json")
}

func Run(ctx context.Context, r Request) (Verdict, error) {
	if err := ValidateReference(r.ImageRef); err != nil {
		return Verdict{}, err
	}
	image, err := r.Catalogue.Find(r.ImageName)
	if err != nil {
		return Verdict{}, err
	}
	budget, err := r.Budgets.For(r.ImageName)
	if err != nil {
		return Verdict{}, err
	}
	vexPath := VEXPath(r.VEXDir, r.ImageName)
	document, err := ReadVEX(vexPath)
	if err != nil {
		return Verdict{}, fmt.Errorf("no usable OpenVEX statements for %s: %w", r.ImageName, err)
	}
	if problems := document.Problems(image); len(problems) > 0 {
		return Verdict{}, fmt.Errorf("%s:\n  %s", vexPath, strings.Join(problems, "\n  "))
	}
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return Verdict{}, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Grype, r.ImageRef, "--output", "json", "--vex", vexPath)
	cmd.Env = append(os.Environ(), GrypeDatabasePolicy...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Verdict{}, fmt.Errorf("grype failed to scan %s: %w\n%s", r.ImageRef, err, strings.TrimSpace(stderr.String()))
	}
	if err := os.WriteFile(filepath.Join(r.OutDir, "grype.json"), stdout.Bytes(), 0o644); err != nil {
		return Verdict{}, fmt.Errorf("cannot write grype.json: %w", err)
	}
	var report grypeReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return Verdict{}, fmt.Errorf("grype produced output this gate cannot parse: %w", err)
	}
	if report.Source == nil || report.Source.Type == "" {
		return Verdict{}, errors.New("grype's report names no scanned source, so it is not a scan of the image")
	}
	verdict := Verdict{Image: r.ImageName, Budget: budget}
	for _, m := range report.Matches {
		switch m.Vulnerability.Severity {
		case "Critical":
			verdict.Critical++
		case "High":
			verdict.High++
		}
	}
	for _, m := range report.IgnoredMatches {
		for _, rule := range m.AppliedIgnoreRules {
			if rule.Namespace == "vex" {
				verdict.Suppressed++
				break
			}
		}
	}
	return verdict, nil
}

func mark(over bool) string {
	if over {
		return "over"
	}
	return "ok"
}

func (v Verdict) Line() string {
	return fmt.Sprintf("cve-gate: %s critical=%d/%d %s  high=%d/%d %s  vex-suppressed=%d  mode=%s",
		v.Image, v.Critical, v.Budget.Critical, mark(v.Critical > v.Budget.Critical),
		v.High, v.Budget.High, mark(v.High > v.Budget.High), v.Suppressed, v.Budget.Mode)
}

const VerdictPredicateType = "https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1"

type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
}

type Scanner struct {
	Name string `json:"name"`
}

type VerdictPredicate struct {
	Image         string  `json:"image"`
	ImageName     string  `json:"imageName"`
	Verdict       string  `json:"verdict"`
	Mode          Mode    `json:"mode"`
	Budget        Counts  `json:"budget"`
	Found         Counts  `json:"found"`
	VEXSuppressed int     `json:"vexSuppressed"`
	Scanner       Scanner `json:"scanner"`
}

func (v Verdict) Predicate(imageRef string) VerdictPredicate {
	verdict := "pass"
	if v.Over() {
		verdict = "fail"
	}
	return VerdictPredicate{
		Image:         imageRef,
		ImageName:     v.Image,
		Verdict:       verdict,
		Mode:          v.Budget.Mode,
		Budget:        Counts{Critical: v.Budget.Critical, High: v.Budget.High},
		Found:         Counts{Critical: v.Critical, High: v.High},
		VEXSuppressed: v.Suppressed,
		Scanner:       Scanner{Name: "grype"},
	}
}

func (v Verdict) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## CVE budget %s\n\n| Severity | Found | Budget | |\n| --- | --- | --- | --- |\n", v.Image)
	fmt.Fprintf(&b, "| Critical | %d | %d | %s |\n", v.Critical, v.Budget.Critical, mark(v.Critical > v.Budget.Critical))
	fmt.Fprintf(&b, "| High | %d | %d | %s |\n\n", v.High, v.Budget.High, mark(v.High > v.Budget.High))
	fmt.Fprintf(&b, "Suppressed by OpenVEX: %d. Mode: %s.\n", v.Suppressed, v.Budget.Mode)
	return b.String()
}

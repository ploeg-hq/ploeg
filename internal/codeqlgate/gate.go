package codeqlgate

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type ExitCode int

const (
	Pass      ExitCode = 0
	Blocked   ExitCode = 1
	NoVerdict ExitCode = 2
)

const BlockingSeverity = 7.0

var dismissalReasons = map[string]bool{"false positive": true, "won't fix": true, "used in tests": true}

type Acceptance struct {
	Rule      string `yaml:"rule" json:"rule"`
	File      string `yaml:"file" json:"file"`
	Dismissal string `yaml:"dismissal" json:"dismissal"`
	Reason    string `yaml:"reason" json:"reason"`
}

type Acceptances struct {
	Accepted []Acceptance `yaml:"accepted"`
}

func LoadAcceptances(path string) (Acceptances, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Acceptances{}, err
	}
	var acceptances Acceptances
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&acceptances); err != nil {
		return Acceptances{}, fmt.Errorf("%s is not valid: %w", path, err)
	}
	seen := map[string]bool{}
	for i, a := range acceptances.Accepted {
		switch {
		case a.Rule == "" || a.File == "":
			return Acceptances{}, fmt.Errorf("%s: entry %d names no rule or file", path, i)
		case strings.TrimSpace(a.Reason) == "":
			return Acceptances{}, fmt.Errorf("%s: %s in %s carries no reason", path, a.Rule, a.File)
		case !dismissalReasons[a.Dismissal]:
			return Acceptances{}, fmt.Errorf("%s: %s in %s has dismissal %q, want false positive, won't fix or used in tests", path, a.Rule, a.File, a.Dismissal)
		case seen[a.key()]:
			return Acceptances{}, fmt.Errorf("%s: %s in %s is listed twice", path, a.Rule, a.File)
		}
		seen[a.key()] = true
	}
	return acceptances, nil
}

func (a Acceptance) key() string { return a.Rule + "\x00" + a.File }

type Finding struct {
	Rule     string
	File     string
	Line     int
	Severity float64
	Message  string
}

func (f Finding) key() string { return f.Rule + "\x00" + f.File }

type sarifRule struct {
	ID         string `json:"id"`
	Properties struct {
		SecuritySeverity string `json:"security-severity"`
	} `json:"properties"`
}

type sarifLog struct {
	Runs []struct {
		Tool struct {
			Driver     struct{ Rules []sarifRule }   `json:"driver"`
			Extensions []struct{ Rules []sarifRule } `json:"extensions"`
		} `json:"tool"`
		Results []struct {
			RuleID       string            `json:"ruleId"`
			Suppressions []json.RawMessage `json:"suppressions"`
			Message      struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

func ReadFindings(paths []string) ([]Finding, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no SARIF file to judge")
	}
	var findings []Finding
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var log sarifLog
		if err := json.Unmarshal(raw, &log); err != nil {
			return nil, fmt.Errorf("%s is not SARIF: %w", path, err)
		}
		if len(log.Runs) == 0 {
			return nil, fmt.Errorf("%s holds no analysis run", path)
		}
		for _, run := range log.Runs {
			severity := map[string]float64{}
			rules := run.Tool.Driver.Rules
			for _, extension := range run.Tool.Extensions {
				rules = append(rules, extension.Rules...)
			}
			for _, rule := range rules {
				if rule.Properties.SecuritySeverity == "" {
					continue
				}
				score, err := strconv.ParseFloat(rule.Properties.SecuritySeverity, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: rule %s has security-severity %q", path, rule.ID, rule.Properties.SecuritySeverity)
				}
				severity[rule.ID] = score
			}
			for _, result := range run.Results {
				if len(result.Suppressions) > 0 {
					continue
				}
				finding := Finding{Rule: result.RuleID, Severity: severity[result.RuleID], Message: result.Message.Text}
				if len(result.Locations) > 0 {
					finding.File = result.Locations[0].PhysicalLocation.ArtifactLocation.URI
					finding.Line = result.Locations[0].PhysicalLocation.Region.StartLine
				}
				findings = append(findings, finding)
			}
		}
	}
	return findings, nil
}

type Scope string

const (
	Complete     Scope = "complete"
	DiffInformed Scope = "diff-informed"
)

type Verdict struct {
	Blocking []Finding
	Accepted []Finding
	Stale    []Acceptance
}

func Judge(findings []Finding, acceptances Acceptances, scope Scope) Verdict {
	accepted := map[string]bool{}
	for _, a := range acceptances.Accepted {
		accepted[a.key()] = true
	}
	matched := map[string]bool{}
	var verdict Verdict
	for _, f := range findings {
		if f.Severity < BlockingSeverity {
			continue
		}
		if accepted[f.key()] {
			matched[f.key()] = true
			verdict.Accepted = append(verdict.Accepted, f)
			continue
		}
		verdict.Blocking = append(verdict.Blocking, f)
	}
	if scope == Complete {
		for _, a := range acceptances.Accepted {
			if !matched[a.key()] {
				verdict.Stale = append(verdict.Stale, a)
			}
		}
	}
	sort.Slice(verdict.Blocking, func(i, j int) bool {
		return verdict.Blocking[i].File+strconv.Itoa(verdict.Blocking[i].Line) < verdict.Blocking[j].File+strconv.Itoa(verdict.Blocking[j].Line)
	})
	return verdict
}

func (v Verdict) Exit() ExitCode {
	if len(v.Blocking) > 0 || len(v.Stale) > 0 {
		return Blocked
	}
	return Pass
}

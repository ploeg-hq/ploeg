package cvegate

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
)

const OpenVEXContext = "https://openvex.dev/ns/v0.2.0"

var justifications = []string{
	"component_not_present",
	"vulnerable_code_not_present",
	"vulnerable_code_not_in_execute_path",
	"vulnerable_code_cannot_be_controlled_by_adversary",
	"inline_mitigations_already_exist",
}

var statuses = []string{"not_affected", "affected", "fixed", "under_investigation"}

type VEXComponent struct {
	ID string `json:"@id"`
}

type VEXProduct struct {
	ID            string         `json:"@id"`
	Subcomponents []VEXComponent `json:"subcomponents,omitempty"`
}

type VEXVulnerability struct {
	Name string `json:"name"`
}

type VEXStatement struct {
	Vulnerability   VEXVulnerability `json:"vulnerability"`
	Timestamp       string           `json:"timestamp,omitempty"`
	Products        []VEXProduct     `json:"products"`
	Status          string           `json:"status"`
	Justification   string           `json:"justification,omitempty"`
	ImpactStatement string           `json:"impact_statement,omitempty"`
	ActionStatement string           `json:"action_statement,omitempty"`
}

type VEXDocument struct {
	Context    string         `json:"@context"`
	ID         string         `json:"@id"`
	Author     string         `json:"author"`
	Timestamp  string         `json:"timestamp"`
	Version    int            `json:"version"`
	Statements []VEXStatement `json:"statements"`
}

func ProductID(image Image) string {
	name := image.Repository[strings.LastIndex(image.Repository, "/")+1:]
	return "pkg:oci/" + name + "?repository_url=" + url.QueryEscape(image.Repository)
}

func DigestProductID(image Image, digest string) string {
	name := image.Repository[strings.LastIndex(image.Repository, "/")+1:]
	return "pkg:oci/" + name + "@" + strings.ReplaceAll(digest, ":", "%3A") + "?repository_url=" + url.QueryEscape(image.Repository)
}

func ReadVEX(path string) (VEXDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return VEXDocument{}, err
	}
	var document VEXDocument
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return VEXDocument{}, fmt.Errorf("%s is not an OpenVEX document this gate accepts: %w", path, err)
	}
	return document, nil
}

func (d VEXDocument) Problems(image Image) []string {
	var problems []string
	if d.Context != OpenVEXContext {
		problems = append(problems, fmt.Sprintf("@context is %q, not %s", d.Context, OpenVEXContext))
	}
	if d.ID == "" || d.Author == "" || d.Timestamp == "" || d.Version < 1 {
		problems = append(problems, "the document needs @id, author, timestamp and a version of at least 1")
	}
	if d.Statements == nil {
		problems = append(problems, "the document has no statements array; write [] when there is nothing to state")
	}
	product := ProductID(image)
	unescapedProduct, _ := url.QueryUnescape(product)
	for i, s := range d.Statements {
		where := fmt.Sprintf("statement %d (%s)", i, s.Vulnerability.Name)
		if s.Vulnerability.Name == "" {
			problems = append(problems, where+" names no vulnerability")
		}
		if !slices.Contains(statuses, s.Status) {
			problems = append(problems, fmt.Sprintf("%s has status %q, not one of %v", where, s.Status, statuses))
		}
		if len(s.Products) == 0 {
			problems = append(problems, where+" names no product, so it would apply to every image")
		}
		for _, p := range s.Products {
			if p.ID != product && p.ID != unescapedProduct {
				problems = append(problems, fmt.Sprintf("%s names product %q; a statement in this file must name %s", where, p.ID, product))
			}
			if s.Status == "not_affected" && len(p.Subcomponents) == 0 {
				problems = append(problems, where+" is not_affected for the whole image; name the package it is about in subcomponents")
			}
			for _, c := range p.Subcomponents {
				if !strings.HasPrefix(c.ID, "pkg:") {
					problems = append(problems, fmt.Sprintf("%s names subcomponent %q, which is not a package URL", where, c.ID))
				}
			}
		}
		switch s.Status {
		case "not_affected":
			if !slices.Contains(justifications, s.Justification) {
				problems = append(problems, fmt.Sprintf("%s is not_affected with justification %q, not one of %v", where, s.Justification, justifications))
			}
			if strings.TrimSpace(s.ImpactStatement) == "" {
				problems = append(problems, where+" is not_affected without an impact_statement saying why")
			}
		case "affected":
			if strings.TrimSpace(s.ActionStatement) == "" {
				problems = append(problems, where+" is affected without an action_statement")
			}
		}
	}
	return problems
}

func (d VEXDocument) ForRelease(image Image, digest, id, timestamp string) VEXDocument {
	release := d
	release.ID = id
	release.Timestamp = timestamp
	release.Statements = make([]VEXStatement, len(d.Statements))
	for i, s := range d.Statements {
		products := make([]VEXProduct, len(s.Products))
		for j, p := range s.Products {
			products[j] = VEXProduct{ID: DigestProductID(image, digest), Subcomponents: p.Subcomponents}
		}
		s.Products = products
		release.Statements[i] = s
	}
	return release
}

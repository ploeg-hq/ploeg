// Package consumers checks the expectations that consumers of Ploeg's
// contracts record against the contracts and routes this Ploeg serves
// (ADR-0147). A consumer keeps one expectation set per release line under
// docs/contracts/consumers/<consumer>/<major>.<minor>.json, at most the two
// newest lines, and every set must hold on every change to Ploeg.
package consumers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// LinesKept is how many release lines of one consumer Ploeg checks: the
// consumer's current line and the one before it.
const LinesKept = 2

var (
	consumerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	lineName     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	methods      = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	jsonTypes    = []string{"array", "boolean", "integer", "null", "number", "object", "string"}
)

// Expectations is what one release line of a consumer relies on.
type Expectations struct {
	SchemaVersion int           `json:"schemaVersion"`
	Consumer      string        `json:"consumer"`
	Line          string        `json:"line"`
	Requires      []Requirement `json:"requires"`
	Interactions  []Interaction `json:"interactions"`
}

// Requirement names a contract and the major version of it a consumer needs.
type Requirement struct {
	Contract string `json:"contract"`
	Version  int    `json:"version"`
}

// Interaction is one request a consumer sends and what it reads from the
// answer. Path uses {name} for a path parameter and carries no query.
type Interaction struct {
	Contract string `json:"contract"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Request  *Body  `json:"request,omitempty"`
	Response string `json:"response,omitempty"`
	Reads    []Read `json:"reads,omitempty"`
}

// Body is a request body: the schema definition it must satisfy and a body
// the consumer actually sends.
type Body struct {
	Definition string          `json:"definition"`
	Example    json.RawMessage `json:"example"`
}

// Read is one value a consumer reads from a response. Pointer is a JSON
// pointer into the response in which * stands for every array item or map
// value. Types are the JSON types the consumer accepts there; Required says
// the consumer needs the value present.
type Read struct {
	Pointer  string   `json:"pointer"`
	Types    []string `json:"types"`
	Required bool     `json:"required,omitempty"`
}

// Name identifies the set in messages, such as "acme 0.4".
func (e Expectations) Name() string { return e.Consumer + " " + e.Line }

// Load reads every expectation set under dir. It refuses an unknown field, a
// file whose consumer or line differs from its path, and a consumer with more
// than LinesKept lines.
func Load(dir string) ([]Expectations, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var sets []Expectations
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		consumer := entry.Name()
		if !consumerName.MatchString(consumer) {
			return nil, fmt.Errorf("%s: a consumer directory is named in lower-case kebab-case", consumer)
		}
		files, err := filepath.Glob(filepath.Join(dir, consumer, "*.json"))
		if err != nil {
			return nil, err
		}
		if len(files) > LinesKept {
			return nil, fmt.Errorf("%s keeps %d release lines; Ploeg checks the newest %d, so remove the oldest", consumer, len(files), LinesKept)
		}
		for _, file := range files {
			set, err := loadFile(file)
			if err != nil {
				return nil, err
			}
			line := strings.TrimSuffix(filepath.Base(file), ".json")
			if set.Consumer != consumer || set.Line != line {
				return nil, fmt.Errorf("%s names consumer %q line %q; its path names %q line %q", file, set.Consumer, set.Line, consumer, line)
			}
			sets = append(sets, set)
		}
	}
	return sets, nil
}

func loadFile(file string) (Expectations, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return Expectations{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var set Expectations
	if err := dec.Decode(&set); err != nil {
		return Expectations{}, fmt.Errorf("%s: %w", file, err)
	}
	if err := set.wellFormed(); err != nil {
		return Expectations{}, fmt.Errorf("%s: %w", file, err)
	}
	return set, nil
}

func (e Expectations) wellFormed() error {
	if e.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1, got %d", e.SchemaVersion)
	}
	if !lineName.MatchString(e.Line) {
		return fmt.Errorf("line %q is not <major>.<minor>", e.Line)
	}
	if len(e.Requires) == 0 || len(e.Interactions) == 0 {
		return errors.New("an expectation set requires at least one contract and records at least one interaction")
	}
	required := map[string]bool{}
	for _, r := range e.Requires {
		if required[r.Contract] || r.Version < 1 {
			return fmt.Errorf("requires %s %d twice or at a version below 1", r.Contract, r.Version)
		}
		required[r.Contract] = true
	}
	for _, in := range e.Interactions {
		if !required[in.Contract] {
			return fmt.Errorf("%s %s uses contract %q, which requires does not name", in.Method, in.Path, in.Contract)
		}
		if !slices.Contains(methods, in.Method) || !strings.HasPrefix(in.Path, "/") || strings.ContainsAny(in.Path, "?#") {
			return fmt.Errorf("%s %s: name an HTTP method and a path without a query", in.Method, in.Path)
		}
		if len(in.Reads) > 0 && in.Response == "" {
			return fmt.Errorf("%s %s reads values but names no response definition", in.Method, in.Path)
		}
		for _, read := range in.Reads {
			if !strings.HasPrefix(read.Pointer, "/") || len(read.Types) == 0 {
				return fmt.Errorf("%s %s: a read needs a JSON pointer and the types the consumer accepts", in.Method, in.Path)
			}
			for _, t := range read.Types {
				if !slices.Contains(jsonTypes, t) {
					return fmt.Errorf("%s %s reads %s as %q, which is not a JSON type", in.Method, in.Path, read.Pointer, t)
				}
			}
		}
	}
	return nil
}

// Provider is the Ploeg an expectation set is checked against.
type Provider struct {
	// ContractsDir holds the published schemas, docs/contracts.
	ContractsDir string
	// Served looks a contract up by name, as contracts.Lookup does.
	Served func(name string) (contracts.Contract, bool)
	// Routes reports whether the server routes a method and a concrete path.
	Routes func(method, path string) bool
}

// Verify returns every way this Ploeg fails the expectation set; none means
// the consumer's recorded use still holds.
func (p Provider) Verify(e Expectations) []error {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(e.Name()+": "+format, args...))
	}
	majors := map[string]int{}
	for _, r := range e.Requires {
		majors[r.Contract] = r.Version
		c, ok := p.Served(r.Contract)
		if !ok || !c.Serves(r.Version) {
			fail("requires %s v%d, which this Ploeg does not serve (it serves %v)", r.Contract, r.Version, c.Versions)
		}
	}
	documents := map[string]*schemaDocument{}
	for _, in := range e.Interactions {
		call := in.Method + " " + in.Path
		if !p.Routes(in.Method, concretePath(in.Path)) {
			fail("%s is not a route this Ploeg serves", call)
		}
		doc, ok := documents[in.Contract]
		if !ok {
			loaded, err := loadSchema(p.ContractsDir, in.Contract, majors[in.Contract])
			if err != nil {
				fail("%s: %v", call, err)
				continue
			}
			documents[in.Contract], doc = loaded, loaded
		}
		if in.Request != nil {
			if err := doc.validate(in.Request.Definition, in.Request.Example); err != nil {
				fail("%s sends a %s that %s no longer accepts: %v", call, in.Request.Definition, doc.label, err)
			}
		}
		if in.Response == "" {
			continue
		}
		root, ok := doc.definition(in.Response)
		if !ok {
			fail("%s reads %s, which %s no longer defines", call, in.Response, doc.label)
			continue
		}
		for _, read := range in.Reads {
			if err := doc.check(root, read); err != nil {
				fail("%s: %s %s: %v", call, in.Response, read.Pointer, err)
			}
		}
	}
	return problems
}

var pathParameter = regexp.MustCompile(`\{[^}/]+\}`)

func concretePath(path string) string { return pathParameter.ReplaceAllString(path, "1") }

type schemaDocument struct {
	label string
	path  string
	root  map[string]any
}

func loadSchema(dir, contract string, major int) (*schemaDocument, error) {
	candidates := []string{filepath.Join(dir, fmt.Sprintf("%s.v%d.schema.json", contract, major))}
	if major == 1 {
		candidates = append(candidates, filepath.Join(dir, contract+".schema.json"))
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var root map[string]any
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		return &schemaDocument{label: contract + " v" + strconv.Itoa(major), path: abs, root: root}, nil
	}
	return nil, fmt.Errorf("no published schema for %s v%d", contract, major)
}

func (d *schemaDocument) definition(name string) (map[string]any, bool) {
	defs, _ := d.root["$defs"].(map[string]any)
	def, ok := defs[name].(map[string]any)
	return def, ok
}

func (d *schemaDocument) validate(definition string, example json.RawMessage) error {
	if _, ok := d.definition(definition); !ok {
		return fmt.Errorf("the definition %s is gone", definition)
	}
	schema, err := jsonschema.NewCompiler().Compile(d.path + "#/$defs/" + definition)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(example))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

func (d *schemaDocument) resolve(node map[string]any) map[string]any {
	for hops := 0; hops < 32; hops++ {
		ref, ok := node["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/$defs/") {
			return node
		}
		next, ok := d.definition(strings.TrimPrefix(ref, "#/$defs/"))
		if !ok {
			return map[string]any{}
		}
		node = next
	}
	return node
}

func (d *schemaDocument) alternatives(node map[string]any) []map[string]any {
	node = d.resolve(node)
	var out []map[string]any
	for _, key := range []string{"anyOf", "oneOf"} {
		list, _ := node[key].([]any)
		for _, each := range list {
			if alt, ok := each.(map[string]any); ok {
				out = append(out, d.alternatives(alt)...)
			}
		}
	}
	if len(out) == 0 {
		return []map[string]any{node}
	}
	return out
}

func (d *schemaDocument) check(root map[string]any, read Read) error {
	nodes := []map[string]any{root}
	segments := strings.Split(read.Pointer, "/")[1:]
	for i, raw := range segments {
		segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		last := i == len(segments)-1
		var next []map[string]any
		requiredEverywhere := true
		for _, node := range nodes {
			for _, alt := range d.alternatives(node) {
				child, required, ok := d.step(alt, segment)
				if !ok {
					continue
				}
				next = append(next, child)
				requiredEverywhere = requiredEverywhere && required
			}
		}
		if len(next) == 0 {
			return fmt.Errorf("the schema no longer has %q there", "/"+strings.Join(segments[:i+1], "/"))
		}
		if last && read.Required && (segment == "*" || !requiredEverywhere) {
			return errors.New("the consumer needs it present, but the schema no longer requires it")
		}
		nodes = next
	}
	sent := map[string]bool{}
	for _, node := range nodes {
		for _, alt := range d.alternatives(node) {
			for _, t := range schemaTypes(alt) {
				sent[t] = true
			}
		}
	}
	var unaccepted []string
	for t := range sent {
		if !accepts(read.Types, t) {
			unaccepted = append(unaccepted, t)
		}
	}
	if len(unaccepted) > 0 {
		slices.Sort(unaccepted)
		return fmt.Errorf("Ploeg may send %v there, which the consumer does not accept (it accepts %v)", unaccepted, read.Types)
	}
	return nil
}

func (d *schemaDocument) step(node map[string]any, segment string) (map[string]any, bool, bool) {
	if segment == "*" {
		if items, ok := node["items"].(map[string]any); ok {
			return items, false, true
		}
		if values, ok := node["additionalProperties"].(map[string]any); ok {
			return values, false, true
		}
		return nil, false, false
	}
	properties, _ := node["properties"].(map[string]any)
	child, ok := properties[segment].(map[string]any)
	if !ok {
		return nil, false, false
	}
	required, _ := node["required"].([]any)
	return child, slices.Contains(required, any(segment)), true
}

func schemaTypes(node map[string]any) []string {
	switch t := node["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, each := range t {
			if s, ok := each.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if value, ok := node["const"]; ok {
		return []string{jsonType(value)}
	}
	if values, ok := node["enum"].([]any); ok {
		var out []string
		for _, value := range values {
			out = append(out, jsonType(value))
		}
		return out
	}
	return []string{"any"}
}

func jsonType(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if v == float64(int64(v)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func accepts(types []string, sent string) bool {
	return slices.Contains(types, sent) || (sent == "integer" && slices.Contains(types, "number"))
}

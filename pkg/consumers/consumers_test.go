package consumers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/contracts"
)

const baseSchema = `{
  "title": "Widget API v1",
  "$defs": {
    "widget": {
      "type": "object",
      "required": ["id", "state"],
      "properties": {
        "id": {"type": "string"},
        "state": {"enum": ["new", "done"]},
        "cost": {"anyOf": [{"type": "number"}, {"type": "null"}]},
        "tags": {"type": "object", "additionalProperties": {"type": "string"}}
      }
    },
    "widgetsResponse": {
      "type": "object",
      "required": ["schemaVersion", "widgets"],
      "properties": {
        "schemaVersion": {"const": "1.0"},
        "widgets": {"type": "array", "items": {"$ref": "#/$defs/widget"}}
      }
    },
    "renameRequest": {
      "type": "object",
      "additionalProperties": false,
      "required": ["name"],
      "properties": {"name": {"type": "string", "maxLength": 20}}
    }
  }
}`

var baseExpectations = Expectations{
	SchemaVersion: 1,
	Consumer:      "acme",
	Line:          "0.4",
	Requires:      []Requirement{{Contract: "widget-api", Version: 1}},
	Interactions: []Interaction{
		{Contract: "widget-api", Method: "GET", Path: "/api/v1/widgets", Response: "widgetsResponse", Reads: []Read{
			{Pointer: "/widgets/*/id", Types: []string{"string"}, Required: true},
			{Pointer: "/widgets/*/state", Types: []string{"string"}},
			{Pointer: "/widgets/*/cost", Types: []string{"number", "null"}},
			{Pointer: "/widgets/*/tags/*", Types: []string{"string"}},
		}},
		{Contract: "widget-api", Method: "POST", Path: "/api/v1/widgets/{id}/rename",
			Request: &Body{Definition: "renameRequest", Example: json.RawMessage(`{"name":"short"}`)}},
	},
}

var baseRoutes = map[string]bool{"GET /api/v1/widgets": true, "POST /api/v1/widgets/1/rename": true}

type provider struct {
	schema func(map[string]any)
	routes map[string]bool
	served []int
}

func (p provider) verify(t *testing.T, e Expectations) []error {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(baseSchema), &doc); err != nil {
		t.Fatal(err)
	}
	if p.schema != nil {
		p.schema(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "widget-api.v1.schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	routes := p.routes
	if routes == nil {
		routes = baseRoutes
	}
	served := p.served
	if served == nil {
		served = []int{1}
	}
	return Provider{
		ContractsDir: dir,
		Served: func(name string) (contracts.Contract, bool) {
			return contracts.Contract{Name: name, Versions: served}, name == "widget-api"
		},
		Routes: func(method, path string) bool { return routes[method+" "+path] },
	}.Verify(e)
}

func defs(doc map[string]any, name string) map[string]any {
	return doc["$defs"].(map[string]any)[name].(map[string]any)
}

func widgetProperties(doc map[string]any) map[string]any {
	return defs(doc, "widget")["properties"].(map[string]any)
}

func TestTheUnchangedContractHoldsTheExpectations(t *testing.T) {
	if problems := (provider{}).verify(t, baseExpectations); len(problems) > 0 {
		t.Fatal(problems)
	}
}

func TestCompatibleChangesPass(t *testing.T) {
	for name, change := range map[string]provider{
		"a new optional response field": {schema: func(doc map[string]any) {
			widgetProperties(doc)["owner"] = map[string]any{"type": "string"}
		}},
		"a new enum value": {schema: func(doc map[string]any) {
			widgetProperties(doc)["state"] = map[string]any{"enum": []any{"new", "done", "archived"}}
		}},
		"a new definition": {schema: func(doc map[string]any) {
			doc["$defs"].(map[string]any)["gadget"] = map[string]any{"type": "object"}
		}},
		"a new optional request field": {schema: func(doc map[string]any) {
			defs(doc, "renameRequest")["properties"].(map[string]any)["note"] = map[string]any{"type": "string"}
		}},
		"a field the consumer does not read becomes required": {schema: func(doc map[string]any) {
			defs(doc, "widget")["required"] = []any{"id", "state", "tags"}
		}},
		"a new route":                            {routes: map[string]bool{"GET /api/v1/widgets": true, "POST /api/v1/widgets/1/rename": true, "GET /api/v1/gadgets": true}},
		"a second major served beside the first": {served: []int{1, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			if problems := change.verify(t, baseExpectations); len(problems) > 0 {
				t.Fatalf("a compatible change failed: %v", problems)
			}
		})
	}
}

func TestIncompatibleChangesFailAndNameWhatBroke(t *testing.T) {
	for name, tc := range map[string]struct {
		change provider
		want   string
	}{
		"a read field removed": {provider{schema: func(doc map[string]any) {
			delete(widgetProperties(doc), "id")
		}}, `no longer has "/widgets/*/id"`},
		"a read field renamed": {provider{schema: func(doc map[string]any) {
			props := widgetProperties(doc)
			props["widgetId"] = props["id"]
			delete(props, "id")
		}}, `no longer has "/widgets/*/id"`},
		"a read field changes type": {provider{schema: func(doc map[string]any) {
			widgetProperties(doc)["id"] = map[string]any{"type": "integer"}
		}}, "may send [integer]"},
		"a read field becomes nullable": {provider{schema: func(doc map[string]any) {
			widgetProperties(doc)["id"] = map[string]any{"type": []any{"string", "null"}}
		}}, "may send [null]"},
		"a needed field is no longer required": {provider{schema: func(doc map[string]any) {
			defs(doc, "widget")["required"] = []any{"state"}
		}}, "no longer requires it"},
		"the response definition removed": {provider{schema: func(doc map[string]any) {
			delete(doc["$defs"].(map[string]any), "widgetsResponse")
		}}, "no longer defines"},
		"a new required request field": {provider{schema: func(doc map[string]any) {
			defs(doc, "renameRequest")["required"] = []any{"name", "reason"}
		}}, "no longer accepts"},
		"a request field the consumer sends is refused": {provider{schema: func(doc map[string]any) {
			defs(doc, "renameRequest")["properties"] = map[string]any{"title": map[string]any{"type": "string"}}
			defs(doc, "renameRequest")["required"] = []any{}
		}}, "no longer accepts"},
		"the route removed":                   {provider{routes: map[string]bool{"GET /api/v1/widgets": true}}, "POST /api/v1/widgets/{id}/rename is not a route"},
		"the required major no longer served": {provider{served: []int{2}}, "requires widget-api v1"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := tc.change.verify(t, baseExpectations)
			if len(problems) == 0 {
				t.Fatal("an incompatible change passed")
			}
			var all []string
			for _, p := range problems {
				all = append(all, p.Error())
			}
			if joined := strings.Join(all, "\n"); !strings.Contains(joined, tc.want) || !strings.HasPrefix(joined, "acme 0.4: ") {
				t.Fatalf("want a problem naming %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

func writeSet(t *testing.T, dir, consumer, line string, mutate func(*Expectations)) {
	t.Helper()
	set := baseExpectations
	set.Consumer, set.Line = consumer, line
	if mutate != nil {
		mutate(&set)
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, consumer), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, consumer, line+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsEachConsumersTwoNewestLines(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, "acme", "0.3", nil)
	writeSet(t, dir, "acme", "0.4", nil)
	writeSet(t, dir, "other", "1.0", nil)
	sets, err := Load(dir)
	if err != nil || len(sets) != 3 {
		t.Fatalf("%d sets, %v", len(sets), err)
	}
}

func TestLoadRefusesWhatItCannotTrust(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, dir string){
		"a third line": func(t *testing.T, dir string) {
			for _, line := range []string{"0.2", "0.3", "0.4"} {
				writeSet(t, dir, "acme", line, nil)
			}
		},
		"a line that differs from the file name": func(t *testing.T, dir string) {
			writeSet(t, dir, "acme", "0.4", func(e *Expectations) { e.Line = "0.3" })
		},
		"an unknown field": func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "acme"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "acme", "0.4.json"), []byte(`{"schemaVersion":1,"consumer":"acme","line":"0.4","surprise":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"an interaction on a contract it does not require": func(t *testing.T, dir string) {
			writeSet(t, dir, "acme", "0.4", func(e *Expectations) {
				e.Interactions = append([]Interaction{}, e.Interactions...)
				e.Interactions[0].Contract = "run-api"
			})
		},
		"a path with a query": func(t *testing.T, dir string) {
			writeSet(t, dir, "acme", "0.4", func(e *Expectations) {
				e.Interactions = append([]Interaction{}, e.Interactions...)
				e.Interactions[0].Path = "/api/v1/widgets?limit=1"
			})
		},
		"a read with an unknown type": func(t *testing.T, dir string) {
			writeSet(t, dir, "acme", "0.4", func(e *Expectations) {
				e.Interactions = []Interaction{{Contract: "widget-api", Method: "GET", Path: "/x", Response: "widgetsResponse",
					Reads: []Read{{Pointer: "/widgets", Types: []string{"list"}}}}}
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			setup(t, dir)
			if _, err := Load(dir); err == nil {
				t.Fatal("loaded")
			}
		})
	}
}

package compat

import (
	"path"
	"sort"
	"strings"
	"testing"
)

type fakeTree map[string]string

func (f fakeTree) read(name string) ([]byte, bool) {
	content, ok := f[name]
	return []byte(content), ok
}

func (f fakeTree) list(dir string) []string {
	var names []string
	for name := range f {
		if path.Dir(name) == dir {
			names = append(names, path.Base(name))
		}
	}
	sort.Strings(names)
	return names
}

func mustSchema(t *testing.T, raw string) any {
	t.Helper()
	value, err := decodeJSON([]byte(raw))
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return value
}

const releasedSchema = `{
  "type": "object",
  "additionalProperties": true,
  "required": ["id"],
  "properties": {
    "id": {"type": "string"},
    "state": {"enum": ["queued", "active", "done"]},
    "cost": {"type": ["number", "null"]},
    "item": {"$ref": "#/$defs/item"},
    "tags": {"type": "array", "items": {"type": "string"}}
  },
  "$defs": {
    "item": {"type": "object", "properties": {"name": {"type": "string"}}},
    "response": {"oneOf": [{"$ref": "#/$defs/item"}, {"$ref": "#/$defs/other"}]},
    "other": {"type": "object"}
  }
}`

func TestSchemaChangesThatBreak(t *testing.T) {
	cases := map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"a property is removed": {
			func(s map[string]any) { delete(s["properties"].(map[string]any), "cost") },
			"/properties/cost: removed",
		},
		"a definition is removed": {
			func(s map[string]any) { delete(s["$defs"].(map[string]any), "other") },
			"/$defs/other: removed",
		},
		"a property becomes required": {
			func(s map[string]any) { s["required"] = []any{"id", "state"} },
			`/required: property "state" became required`,
		},
		"a type changes": {
			func(s map[string]any) { prop(s, "id")["type"] = "integer" },
			`/properties/id/type: changed from "string" to "integer"`,
		},
		"null is added to a type": {
			func(s map[string]any) { prop(s, "id")["type"] = []any{"string", "null"} },
			`/properties/id/type: changed from "string" to ["string","null"]`,
		},
		"an enum value is removed": {
			func(s map[string]any) { prop(s, "state")["enum"] = []any{"queued", "done"} },
			`/properties/state/enum: value "active" removed`,
		},
		"unknown properties become refused": {
			func(s map[string]any) { s["additionalProperties"] = false },
			"/additionalProperties: unknown properties are now refused",
		},
		"a reference moves": {
			func(s map[string]any) { prop(s, "item")["$ref"] = "#/$defs/other" },
			`/properties/item/$ref: changed from "#/$defs/item" to "#/$defs/other"`,
		},
		"a nested item type changes": {
			func(s map[string]any) { prop(s, "tags")["items"] = map[string]any{"type": "integer"} },
			`/properties/tags/items/type: changed from "string" to "integer"`,
		},
		"a oneOf alternative is removed": {
			func(s map[string]any) {
				s["$defs"].(map[string]any)["response"] = map[string]any{"oneOf": []any{map[string]any{"$ref": "#/$defs/item"}}}
			},
			"/$defs/response/oneOf: alternative #/$defs/other removed",
		},
		"a field becomes nullable through anyOf": {
			func(s map[string]any) {
				s["properties"].(map[string]any)["item"] = map[string]any{"anyOf": []any{map[string]any{"$ref": "#/$defs/item"}, map[string]any{"type": "null"}}}
			},
			"/properties/item/anyOf: alternatives added, so a reader can get a form it never handled",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			old := mustSchema(t, releasedSchema)
			changed := mustSchema(t, releasedSchema).(map[string]any)
			c.edit(changed)
			breaks := schemaBreaks(old, changed)
			if !containsBreak(breaks, c.want) {
				t.Errorf("want %q among %v", c.want, breaks)
			}
		})
	}
}

func TestSchemaChangesThatStayCompatible(t *testing.T) {
	cases := map[string]func(map[string]any){
		"nothing changes": func(map[string]any) {},
		"an optional property is added": func(s map[string]any) {
			s["properties"].(map[string]any)["note"] = map[string]any{"type": "string"}
		},
		"a definition is added": func(s map[string]any) {
			s["$defs"].(map[string]any)["extra"] = map[string]any{"type": "object"}
		},
		"an enum value is added": func(s map[string]any) {
			prop(s, "state")["enum"] = []any{"queued", "active", "done", "stale"}
		},
		"a description and a deprecation mark change": func(s map[string]any) {
			prop(s, "cost")["description"] = "Use heldUsd."
			prop(s, "cost")["deprecated"] = true
		},
		"a property stops being required": func(s map[string]any) { s["required"] = []any{} },
		"the type list is reordered":      func(s map[string]any) { prop(s, "cost")["type"] = []any{"null", "number"} },
		"a oneOf alternative is added": func(s map[string]any) {
			response := s["$defs"].(map[string]any)["response"].(map[string]any)
			response["oneOf"] = append(response["oneOf"].([]any), map[string]any{"$ref": "#/$defs/extra"})
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			old := mustSchema(t, releasedSchema)
			changed := mustSchema(t, releasedSchema).(map[string]any)
			edit(changed)
			if breaks := schemaBreaks(old, changed); len(breaks) != 0 {
				t.Errorf("want no breaks, got %v", breaks)
			}
		})
	}
}

func prop(schema map[string]any, name string) map[string]any {
	return schema["properties"].(map[string]any)[name].(map[string]any)
}

func containsBreak(breaks []schemaBreak, want string) bool {
	for _, b := range breaks {
		if b.String() == want {
			return true
		}
	}
	return false
}

func surfaceStrings(breaks []surfaceBreak) []string {
	var out []string
	for _, b := range breaks {
		out = append(out, b.String())
	}
	return out
}

func TestMigrationsAreAppendOnly(t *testing.T) {
	released := fakeTree{
		"pkg/store/migrations/0001_init.sql":  "CREATE TABLE a ();",
		"pkg/store/migrations/0002_more.sql":  "ALTER TABLE a ADD b int;",
		"pkg/store/migrations/0004_later.sql": "SELECT 1;",
	}
	cases := map[string]struct {
		current fakeTree
		want    []string
	}{
		"a new migration after the last": {
			fakeTree{
				"pkg/store/migrations/0001_init.sql":  "CREATE TABLE a ();",
				"pkg/store/migrations/0002_more.sql":  "ALTER TABLE a ADD b int;",
				"pkg/store/migrations/0004_later.sql": "SELECT 1;",
				"pkg/store/migrations/0005_new.sql":   "SELECT 2;",
			},
			nil,
		},
		"an edited, a removed and a back-dated migration": {
			fakeTree{
				"pkg/store/migrations/0001_init.sql":  "CREATE TABLE a (id int);",
				"pkg/store/migrations/0003_gap.sql":   "SELECT 3;",
				"pkg/store/migrations/0004_later.sql": "SELECT 1;",
			},
			[]string{
				"pkg/store/migrations#0001_init.sql: released migration edited; add a new migration instead",
				"pkg/store/migrations#0002_more.sql: released migration removed",
				"pkg/store/migrations#0003_gap.sql: new migration sorts before the released 0004_later.sql, so an upgraded database applies it after that one and a new database before it",
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := surfaceStrings(migrationBreaks(released, c.current))
			sort.Strings(got)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestEnvironmentConfigAndMCPRemovalsBreak(t *testing.T) {
	released := fakeTree{
		configurationPath:           "| `PLOEG_KEEP` | ploegd | | | |\n| `PLOEG_GONE` | ploeg-worker | | | |\n",
		"pkg/config/config.go":      "type T struct {\n\tKeep string `yaml:\"keep\"`\n\tGone string `yaml:\"gone,omitempty\"`\n}\n",
		"pkg/config/config_test.go": "type X struct { Y string `yaml:\"testonly\"` }\n",
		"cmd/ploeg-mcp/tools.go":    "mcp.AddTool(srv, &mcp.Tool{Name: \"ploeg_keep\"})\nmcp.AddTool(srv, &mcp.Tool{Name: \"ploeg_gone\"})\n",
	}
	current := fakeTree{
		configurationPath:        "| `PLOEG_KEEP` | ploegd | | | |\n| `PLOEG_NEW` | ploegd | | | |\n",
		"pkg/config/config.go":   "type T struct {\n\tKeep string `yaml:\"keep\"`\n\tNew string `yaml:\"new\"`\n}\n",
		"cmd/ploeg-mcp/tools.go": "mcp.AddTool(srv, &mcp.Tool{Name: \"ploeg_keep\"})\nmcp.AddTool(srv, &mcp.Tool{Name: \"ploeg_new\"})\n",
	}
	breaks, err := compatibilityBreaks(released, current)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"config#gone: configuration key no longer accepted; a file that sets it fails to load",
		"environment#ploeg-worker/PLOEG_GONE: variable no longer read",
		"mcp#ploeg_gone: tool removed",
	}
	if got := surfaceStrings(breaks); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if breaks, _ := compatibilityBreaks(released, released); len(breaks) != 0 {
		t.Errorf("an unchanged tree reports %v", surfaceStrings(breaks))
	}
}

func TestARemovedSchemaFileBreaks(t *testing.T) {
	released := fakeTree{"docs/contracts/run-api.v1.schema.json": `{"type":"object"}`, valuesSchemaPath: `{"type":"object"}`}
	current := fakeTree{valuesSchemaPath: `{"type":"object"}`}
	breaks, err := compatibilityBreaks(released, current)
	if err != nil {
		t.Fatal(err)
	}
	want := "docs/contracts/run-api.v1.schema.json#: schema file removed"
	if got := surfaceStrings(breaks); len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want [%s]", got, want)
	}
}

func TestARecordedExceptionExcusesOnlyItsBaseAndPointer(t *testing.T) {
	b := surfaceBreak{surface: "docs/contracts/operator-api.v1.schema.json", pointer: "/$defs/card/properties/grade", reason: "removed"}
	exceptions := []exception{{Base: "v0.2.0-rc.47", Surface: b.surface, Pointer: "/$defs/card", ADR: "0080"}}
	if !excused(b, "v0.2.0-rc.47", exceptions) {
		t.Error("a break under the recorded pointer and base is not excused")
	}
	if excused(b, "v0.2.0-rc.48", exceptions) {
		t.Error("an exception recorded for an older base still excuses")
	}
	if excused(surfaceBreak{surface: b.surface, pointer: "/$defs/cardinal", reason: "removed"}, "v0.2.0-rc.47", exceptions) {
		t.Error("a pointer that only shares a prefix string is excused")
	}
}

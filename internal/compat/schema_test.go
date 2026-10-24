package compat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type schemaBreak struct {
	pointer string
	reason  string
}

func (b schemaBreak) String() string { return b.pointer + ": " + b.reason }

var definitionKeywords = []string{"$defs", "definitions"}

var nestedSchemaKeywords = []string{"items", "additionalProperties", "not", "if", "then", "else", "contains", "propertyNames"}

var schemaListKeywords = []string{"oneOf", "anyOf", "allOf", "prefixItems"}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func schemaBreaks(old, new any) []schemaBreak {
	var breaks []schemaBreak
	compareSchema("", old, new, &breaks)
	sort.Slice(breaks, func(i, j int) bool { return breaks[i].String() < breaks[j].String() })
	return breaks
}

func compareSchema(pointer string, oldValue, newValue any, breaks *[]schemaBreak) {
	report := func(at, reason string) { *breaks = append(*breaks, schemaBreak{pointer: at, reason: reason}) }
	old, oldIsObject := oldValue.(map[string]any)
	new, newIsObject := newValue.(map[string]any)
	if !oldIsObject || !newIsObject {
		if !reflect.DeepEqual(oldValue, newValue) {
			report(pointer, "schema changed from "+compactJSON(oldValue)+" to "+compactJSON(newValue))
		}
		return
	}
	for _, keyword := range []string{"type", "$ref", "const", "format"} {
		if !reflect.DeepEqual(typeSet(old[keyword]), typeSet(new[keyword])) {
			report(pointer+"/"+keyword, "changed from "+compactJSON(old[keyword])+" to "+compactJSON(new[keyword]))
		}
	}
	for _, value := range missing(stringList(old["enum"]), stringList(new["enum"])) {
		report(pointer+"/enum", "value "+value+" removed")
	}
	if _, had := old["enum"]; !had {
		if _, has := new["enum"]; has {
			report(pointer+"/enum", "enum added to a schema that accepted any value")
		}
	}
	for _, name := range missing(stringList(new["required"]), stringList(old["required"])) {
		report(pointer+"/required", "property "+name+" became required")
	}
	if old["additionalProperties"] != false && new["additionalProperties"] == false {
		report(pointer+"/additionalProperties", "unknown properties are now refused")
	}
	compareProperties(pointer+"/properties", old["properties"], new["properties"], breaks)
	compareProperties(pointer+"/patternProperties", old["patternProperties"], new["patternProperties"], breaks)
	for _, keyword := range definitionKeywords {
		compareProperties(pointer+"/"+keyword, old[keyword], new[keyword], breaks)
	}
	for _, keyword := range nestedSchemaKeywords {
		oldChild, hadChild := old[keyword]
		newChild, hasChild := new[keyword]
		switch {
		case hadChild && hasChild:
			compareSchema(pointer+"/"+keyword, oldChild, newChild, breaks)
		case !hadChild && hasChild && keyword != "additionalProperties":
			report(pointer+"/"+keyword, "constraint added")
		}
	}
	for _, keyword := range schemaListKeywords {
		compareAlternatives(pointer+"/"+keyword, keyword, old[keyword], new[keyword], breaks)
	}
}

func refsOnly(list []any) (map[string]bool, bool) {
	refs := map[string]bool{}
	for _, item := range list {
		object, ok := item.(map[string]any)
		ref, isString := object["$ref"].(string)
		if !ok || !isString || len(object) != 1 {
			return nil, false
		}
		refs[ref] = true
	}
	return refs, true
}

func compareAlternatives(pointer, keyword string, oldValue, newValue any, breaks *[]schemaBreak) {
	report := func(reason string) { *breaks = append(*breaks, schemaBreak{pointer: pointer, reason: reason}) }
	oldList, _ := oldValue.([]any)
	newList, _ := newValue.([]any)
	widens := keyword == "oneOf" || keyword == "anyOf"
	switch {
	case len(oldList) == 0 && len(newList) == 0:
		return
	case len(oldList) == 0 && widens:
		report("alternatives added, so a reader can get a form it never handled")
		return
	case len(oldList) == 0:
		report("constraint added")
		return
	}
	oldRefs, oldAreRefs := refsOnly(oldList)
	newRefs, newAreRefs := refsOnly(newList)
	if oldAreRefs && newAreRefs {
		for ref := range oldRefs {
			if !newRefs[ref] {
				report("alternative " + ref + " removed")
			}
		}
		if !widens {
			for ref := range newRefs {
				if !oldRefs[ref] {
					report("constraint " + ref + " added")
				}
			}
		}
		return
	}
	if len(newList) < len(oldList) || (!widens && len(newList) != len(oldList)) {
		report(fmt.Sprintf("%d alternatives became %d", len(oldList), len(newList)))
		return
	}
	for i := range oldList {
		compareSchema(pointer+"/"+strconv.Itoa(i), oldList[i], newList[i], breaks)
	}
}

func compareProperties(pointer string, oldValue, newValue any, breaks *[]schemaBreak) {
	old, _ := oldValue.(map[string]any)
	new, _ := newValue.(map[string]any)
	for name, oldChild := range old {
		at := pointer + "/" + escapePointer(name)
		newChild, ok := new[name]
		if !ok {
			*breaks = append(*breaks, schemaBreak{pointer: at, reason: "removed"})
			continue
		}
		compareSchema(at, oldChild, newChild, breaks)
	}
}

func typeSet(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	names := stringList(list)
	sort.Strings(names)
	return names
}

func stringList(value any) []string {
	list, _ := value.([]any)
	names := make([]string, 0, len(list))
	for _, item := range list {
		names = append(names, compactJSON(item))
	}
	return names
}

func missing(from, in []string) []string {
	present := map[string]bool{}
	for _, item := range in {
		present[item] = true
	}
	var gone []string
	for _, item := range from {
		if !present[item] {
			gone = append(gone, item)
		}
	}
	return gone
}

func compactJSON(value any) string {
	if value == nil {
		return "nothing"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

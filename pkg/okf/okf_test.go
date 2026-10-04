package okf

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const orders = `---
type: BigQuery Table
title: Orders
description: One row per completed customer order.
resource: https://console.cloud.google.com/bigquery?p=acme&d=sales&t=orders
tags: [sales, revenue]
timestamp: 2026-05-28T14:30:00Z
owner: data-team
---

# Schema

| order_id | FK to [customers](/tables/customers.md) |

Joined with [customers](customers.md#keys) on customer_id, see [the metric](../metrics/wau.md),
[the console](https://example.test/x.md) and [outside](../../escape.md).
`

func TestParse_ReadsTheReservedFieldsAndKeepsTheRest(t *testing.T) {
	c, err := Parse("tables/orders.md", []byte(orders))
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != "BigQuery Table" || c.Title != "Orders" || c.Description != "One row per completed customer order." {
		t.Errorf("reserved fields = %q %q %q", c.Type, c.Title, c.Description)
	}
	if !reflect.DeepEqual(c.Tags, []string{"sales", "revenue"}) {
		t.Errorf("tags = %v", c.Tags)
	}
	if !c.Timestamp.Equal(time.Date(2026, 5, 28, 14, 30, 0, 0, time.UTC)) {
		t.Errorf("timestamp = %v", c.Timestamp)
	}
	if c.Extra["owner"] != "data-team" {
		t.Errorf("extra = %v", c.Extra)
	}
	if want := []string{"metrics/wau.md", "tables/customers.md"}; !reflect.DeepEqual(c.Links, want) {
		t.Errorf("links = %v, want %v (absolute and relative resolved, external and escaping dropped)", c.Links, want)
	}
}

func TestParse_RequiresTypeAndFrontmatter(t *testing.T) {
	for name, doc := range map[string]string{
		"no frontmatter": "# just markdown\n",
		"no type":        "---\ntitle: x\n---\nbody\n",
		"unclosed":       "---\ntype: x\nbody\n",
		"invalid yaml":   "---\ntype: x\ndescription: a: b\n---\n",
	} {
		if _, err := Parse("a.md", []byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestFormat_RoundTrips(t *testing.T) {
	c, err := Parse("tables/orders.md", []byte(orders))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(c.Path, Format(c))
	if err != nil {
		t.Fatalf("formatted concept does not parse: %v\n%s", err, Format(c))
	}
	c.Body, again.Body = strings.TrimSpace(c.Body), strings.TrimSpace(again.Body)
	if !reflect.DeepEqual(c, again) {
		t.Errorf("round trip changed the concept:\n%+v\n%+v", c, again)
	}
}

func TestFormat_QuotesValuesYAMLWouldMisread(t *testing.T) {
	c := Concept{Path: "a.md", Type: "Pitfall", Title: "ADR-0011: the blackboard", Tags: []string{"a: b", "yes"}, Body: "x"}
	again, err := Parse("a.md", Format(c))
	if err != nil {
		t.Fatalf("%v\n%s", err, Format(c))
	}
	if again.Title != c.Title || !reflect.DeepEqual(again.Tags, c.Tags) {
		t.Errorf("got %q %v", again.Title, again.Tags)
	}
}

func TestReadBundle_ReportsNonConformingFilesAndSkipsReservedOnes(t *testing.T) {
	fsys := fstest.MapFS{
		"index.md":            {Data: []byte("# index, no frontmatter\n")},
		"log.md":              {Data: []byte("# log\n")},
		"tables/orders.md":    {Data: []byte(orders)},
		"tables/customers.md": {Data: []byte("---\ntype: BigQuery Table\n---\n")},
		"tables/broken.md":    {Data: []byte("no frontmatter")},
		"tables/notes.txt":    {Data: []byte("ignored")},
		".hidden/secret.md":   {Data: []byte("---\ntype: x\n---\n")},
		"metrics/wau.md":      {Data: []byte("---\ntype: Metric\n---\n")},
	}
	b, problems, err := ReadBundle("sales", fsys)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range b.Concepts {
		paths = append(paths, c.Path)
	}
	if want := []string{"metrics/wau.md", "tables/customers.md", "tables/orders.md"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("concepts = %v, want %v", paths, want)
	}
	if len(problems) != 1 || problems[0].Path != "tables/broken.md" {
		t.Errorf("problems = %v", problems)
	}
	if d := b.Dangling(); len(d) != 0 {
		t.Errorf("dangling = %v", d)
	}
}

func TestWrite_WritesConceptsAndAnIndexPerDirectory(t *testing.T) {
	dir := t.TempDir()
	concepts := []Concept{
		{Path: "a/b/deep.md", Type: "Fact", Title: "Deep", Description: "down there", Body: "x"},
		{Path: "top.md", Type: "Fact", Body: "y"},
	}
	if err := Write(dir, "Test", concepts); err != nil {
		t.Fatal(err)
	}
	root, _ := os.ReadFile(filepath.Join(dir, IndexFile))
	if !strings.Contains(string(root), "[top](top.md) (Fact)") || !strings.Contains(string(root), "[a/](a/index.md)") {
		t.Errorf("root index:\n%s", root)
	}
	mid, _ := os.ReadFile(filepath.Join(dir, "a", IndexFile))
	if !strings.Contains(string(mid), "[b/](b/index.md)") {
		t.Errorf("a/index.md:\n%s", mid)
	}
	leaf, _ := os.ReadFile(filepath.Join(dir, "a", "b", IndexFile))
	if !strings.Contains(string(leaf), "[Deep](deep.md) (Fact): down there") {
		t.Errorf("a/b/index.md:\n%s", leaf)
	}
	b, problems, err := ReadDir(dir)
	if err != nil || len(problems) != 0 || len(b.Concepts) != 2 {
		t.Errorf("written bundle reads back as %d concepts, problems %v, err %v", len(b.Concepts), problems, err)
	}
}

func TestWrite_RefusesPathsThatLeaveTheBundle(t *testing.T) {
	for _, p := range []string{"../x.md", "/abs.md", "a/../../x.md", "a/./b.md", "index.md", "x.txt", ""} {
		if err := Write(t.TempDir(), "t", []Concept{{Path: p, Type: "x"}}); err == nil {
			t.Errorf("%q written", p)
		}
	}
}

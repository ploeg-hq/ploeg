package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/ploeg-hq/ploeg/pkg/config"
)

func TestWarnRetiredKeysNamesEachKey(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &config.File{Cards: yaml.Node{Kind: yaml.MappingNode},
		Teams: map[string]config.Team{"silver": {WorkingHours: yaml.Node{Kind: yaml.MappingNode}}}}
	warnRetiredKeys(log, cfg)
	out := buf.String()
	if strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "key=cards") || !strings.Contains(out, "key=teams.silver.workingHours") {
		t.Fatalf("log = %s", out)
	}
	buf.Reset()
	warnRetiredKeys(log, &config.File{})
	if buf.Len() != 0 {
		t.Fatalf("a current configuration warned: %s", buf.String())
	}
}

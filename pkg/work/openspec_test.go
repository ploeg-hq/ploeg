package work

import (
	"strings"
	"testing"
)

func TestOpenSpecChange(t *testing.T) {
	cases := []struct {
		name, description, want, wantErr string
	}{
		{name: "none", description: "Fix the thing.\nIt is broken.", want: ""},
		{name: "plain", description: "Implement it.\n\nopenspec: add-widget\n", want: "add-widget"},
		{name: "key is case-insensitive", description: "OpenSpec: add-widget", want: "add-widget"},
		{name: "backticks", description: "openspec: `add-widget`", want: "add-widget"},
		{name: "vikunja html", description: "<p>Implement it.</p><p>OpenSpec: <code>add-widget</code></p>", want: "add-widget"},
		{name: "html line break", description: "<p>Implement it.<br>openspec: add-widget</p>", want: "add-widget"},
		{name: "html entity", description: "<p>openspec:&nbsp;add-widget</p>", want: "add-widget"},
		{name: "repeated same id", description: "openspec: add-widget\nopenspec: add-widget", want: "add-widget"},
		{name: "prose mention is not a directive", description: "We use openspec: see the docs for add-widget.", want: ""},
		{name: "traversal", description: "openspec: ../secrets", wantErr: "kebab-case"},
		{name: "flag", description: "openspec: --all", wantErr: "kebab-case"},
		{name: "uppercase", description: "openspec: Add-Widget", wantErr: "kebab-case"},
		{name: "empty", description: "openspec:", wantErr: "kebab-case"},
		{name: "too long", description: "openspec: " + strings.Repeat("a", MaxOpenSpecChangeLen+1), wantErr: "at most"},
		{name: "conflicting", description: "openspec: add-widget\nopenspec: remove-widget", wantErr: "two OpenSpec changes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := OpenSpecChange(c.description)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("OpenSpecChange(%q) error = %v, want one containing %q", c.description, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenSpecChange(%q) error = %v", c.description, err)
			}
			if got != c.want {
				t.Fatalf("OpenSpecChange(%q) = %q, want %q", c.description, got, c.want)
			}
		})
	}
}

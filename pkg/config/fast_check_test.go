package config

import (
	"slices"
	"strings"
	"testing"
)

func TestFastChecks_ReadEveryRouteThatDeclaresOne(t *testing.T) {
	f, err := Load(write(t, `
targets:
  shop:
    repo: acme/shop
    forge: acme
    fastCheck:
      - go vet ./...
      - go test -short ./...
trackers:
  vikunja:
    projects:
      - name: "Site"
        repo: acme/site
        fastCheck: [npm run lint]
      - name: "Docs"
        repo: acme/docs
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.FastChecks()
	if len(got) != 2 {
		t.Fatalf("FastChecks = %+v, want the two routes that declare one", got)
	}
	if got[0].Forge != "" || got[0].Repo != "acme/site" || !slices.Equal(got[0].Commands, []string{"npm run lint"}) {
		t.Errorf("FastChecks[0] = %+v", got[0])
	}
	if got[1].Forge != "acme" || got[1].Repo != "acme/shop" || !slices.Equal(got[1].Commands, []string{"go vet ./...", "go test -short ./..."}) {
		t.Errorf("FastChecks[1] = %+v", got[1])
	}
}

func TestFastChecks_RefuseACheckThatCannotRun(t *testing.T) {
	for name, yaml := range map[string]string{
		"an empty command": `
targets:
  shop:
    repo: acme/shop
    fastCheck: ["  "]
`,
		"a command over two lines": `
targets:
  shop:
    repo: acme/shop
    fastCheck: ["go vet ./...\ngo test ./..."]
`,
		"two checks for one repository": `
targets:
  shop:
    repo: acme/shop
    fastCheck: [go vet ./...]
trackers:
  vikunja:
    projects:
      - name: "Shop"
        repo: acme/shop
        fastCheck: [go test ./...]
`,
		"a project without a repository": `
targets:
  shop:
    repo: acme/shop
trackers:
  vikunja:
    projects:
      - name: "Shop"
        default: shop
        fastCheck: [go test ./...]
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, yaml)); err == nil || !strings.Contains(err.Error(), "fastCheck") {
				t.Errorf("err = %v, want a fastCheck error", err)
			}
		})
	}
}

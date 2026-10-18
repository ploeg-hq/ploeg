package config

import (
	"strings"
	"testing"
	"time"
)

func TestCIWaits_ReadEveryRouteThatSetsAWaitTimeout(t *testing.T) {
	f, err := Load(write(t, `
targets:
  ploeg:
    repo: webgrip/ploeg
    forge: webgrip
    ci:
      waitTimeout: 90m
trackers:
  vikunja:
    projects:
      - name: "Site"
        repo: webgrip/site
        ci:
          waitTimeout: 15m
      - name: "Docs"
        repo: webgrip/docs
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.CIWaits()
	want := []CIWait{{Repo: "webgrip/site", Timeout: 15 * time.Minute}, {Forge: "webgrip", Repo: "webgrip/ploeg", Timeout: 90 * time.Minute}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("CIWaits = %+v, want %+v", got, want)
	}
}

func TestCIWaits_RefuseATimeoutThatCannotBeRead(t *testing.T) {
	for name, yaml := range map[string]string{
		"not a duration": `
targets:
  ploeg:
    repo: webgrip/ploeg
    ci: {waitTimeout: soon}
`,
		"not positive": `
targets:
  ploeg:
    repo: webgrip/ploeg
    ci: {waitTimeout: 0s}
`,
		"two timeouts for one repository": `
targets:
  ploeg:
    repo: webgrip/ploeg
    ci: {waitTimeout: 30m}
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        repo: webgrip/ploeg
        ci: {waitTimeout: 60m}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, yaml)); err == nil || !strings.Contains(err.Error(), "ci.waitTimeout") {
				t.Errorf("err = %v, want a ci.waitTimeout error", err)
			}
		})
	}
}

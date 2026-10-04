package config

import (
	"strings"
	"testing"
)

func TestCardStyles_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  app:
    repo: webgrip/App
    cardStyle:
      theme: acme
  homelab:
    repo: webgrip/homelab-cluster
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        cardStyle:
          skin: foil
          theme: client-b
      - name: "App"
        id: "10"
        default: app
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	styles, err := f.CardStyles()
	if err != nil {
		t.Fatal(err)
	}
	if got := styles["webgrip/app"]; got != (CardStyle{Skin: DefaultCardSkin, Theme: "acme"}) {
		t.Errorf("app = %+v; an omitted skin is the default skin", got)
	}
	if got := styles["webgrip/ploeg"]; got != (CardStyle{Skin: "foil", Theme: "client-b"}) {
		t.Errorf("ploeg = %+v", got)
	}
	if _, ok := styles["webgrip/homelab-cluster"]; ok || len(styles) != 2 {
		t.Errorf("styles = %+v; a target without cardStyle is not listed", styles)
	}
}

func TestCardStyles_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"bad skin": {`
targets:
  app:
    repo: webgrip/app
    cardStyle: {skin: "Foil Edition"}
`, "targets.app.cardStyle: skin"},
		"bad theme": {`
targets:
  app:
    repo: webgrip/app
    cardStyle: {theme: "../x"}
`, "theme"},
		"unknown key": {`
targets:
  app:
    repo: webgrip/app
    cardStyle: {rarity: holo}
`, "rarity"},
		"style without repo": {`
targets:
  app:
    repo: webgrip/app
trackers:
  vikunja:
    projects:
      - name: "App"
        default: app
        cardStyle: {skin: foil}
`, "cardStyle requires repo"},
		"conflicting styles": {`
targets:
  app:
    repo: webgrip/app
    cardStyle: {skin: foil}
trackers:
  vikunja:
    projects:
      - name: "App"
        repo: WebGrip/App
        cardStyle: {skin: matte}
`, "differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestCardStyles_TheSameStyleTwiceIsAllowed(t *testing.T) {
	_, err := Load(write(t, `
targets:
  app:
    repo: webgrip/app
    cardStyle: {skin: default, theme: acme}
trackers:
  vikunja:
    projects:
      - name: "App"
        repo: webgrip/app
        cardStyle: {theme: acme}
`))
	if err != nil {
		t.Fatalf("an explicit default skin and an omitted one are the same style: %v", err)
	}
}

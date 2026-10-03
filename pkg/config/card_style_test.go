package config

import (
	"strings"
	"testing"
)

func TestCardStyles_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  glide:
    repo: webgrip/Glide
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
      - name: "Glide"
        id: "10"
        default: glide
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	styles, err := f.CardStyles()
	if err != nil {
		t.Fatal(err)
	}
	if got := styles["webgrip/glide"]; got != (CardStyle{Skin: DefaultCardSkin, Theme: "acme"}) {
		t.Errorf("glide = %+v; an omitted skin is the default skin", got)
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
  glide:
    repo: webgrip/glide
    cardStyle: {skin: "Foil Edition"}
`, "targets.glide.cardStyle: skin"},
		"bad theme": {`
targets:
  glide:
    repo: webgrip/glide
    cardStyle: {theme: "../x"}
`, "theme"},
		"unknown key": {`
targets:
  glide:
    repo: webgrip/glide
    cardStyle: {rarity: holo}
`, "rarity"},
		"style without repo": {`
targets:
  glide:
    repo: webgrip/glide
trackers:
  vikunja:
    projects:
      - name: "Glide"
        default: glide
        cardStyle: {skin: foil}
`, "cardStyle requires repo"},
		"conflicting styles": {`
targets:
  glide:
    repo: webgrip/glide
    cardStyle: {skin: foil}
trackers:
  vikunja:
    projects:
      - name: "Glide"
        repo: WebGrip/Glide
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
  glide:
    repo: webgrip/glide
    cardStyle: {skin: vloer-native, theme: acme}
trackers:
  vikunja:
    projects:
      - name: "Glide"
        repo: webgrip/glide
        cardStyle: {theme: acme}
`))
	if err != nil {
		t.Fatalf("an explicit default skin and an omitted one are the same style: %v", err)
	}
}

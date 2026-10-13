package config

import (
	"strings"
	"testing"
)

func TestRunCardKeysAreUnknownKeys(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"cards", "cards:\n  enabled: false\n"},
		{"target cardStyle", "targets:\n  app:\n    repo: webgrip/app\n    cardStyle: {skin: default}\n"},
		{"target release", "targets:\n  app:\n    repo: webgrip/app\n    release: {environment: live}\n"},
		{"target rarity", "targets:\n  app:\n    repo: webgrip/app\n    rarity: {sizeExclude: []}\n"},
		{"target cardShape", "targets:\n  app:\n    repo: webgrip/app\n    cardShape: {docPaths: []}\n"},
		{"project cardStyle", "trackers:\n  vikunja:\n    projects:\n      - name: \"Ploeg\"\n        id: \"11\"\n        repo: webgrip/ploeg\n        cardStyle: {skin: default}\n"},
		{"project statusKinds", "trackers:\n  vikunja:\n    projects:\n      - name: \"Ploeg\"\n        id: \"11\"\n        repo: webgrip/ploeg\n        statusKinds: {blocked: [\"Parked\"]}\n"},
		{"team cards", "teams:\n  silver:\n    cards: {prComment: true}\n"},
		{"team workingHours", "teams:\n  silver:\n    workingHours: {timezone: UTC}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("a Run card key must fail the boot as an unknown key, got %v", err)
			}
		})
	}
}

func TestAnUnknownKeyStillFailsAtLoad(t *testing.T) {
	if _, err := Load(write(t, "teams:\n  silver:\n    cardz: {}\n")); err == nil {
		t.Fatal("a typo'd key must still be a boot failure")
	}
}

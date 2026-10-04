package harness

import (
	"strings"
	"testing"
)

func TestPullRequestNumber(t *testing.T) {
	for in, want := range map[string]int{
		"https://forgejo.example/o/ploeg/pulls/7":           7,
		"https://forgejo.example/o/ploeg/pulls/7/":          7,
		"https://github.com/o/r/pull/123":                   123,
		"https://gitlab.example/g/sub/p/-/merge_requests/5": 5,
		"https://forgejo/o/ploeg/issues/7":                  0,
		"https://forgejo/o/ploeg":                           0,
		"https://forgejo/o/ploeg/pulls/0":                   0,
		"":                                                  0,
		"not a url":                                         0,
	} {
		if got := PullRequestNumber(in); got != want {
			t.Errorf("PullRequestNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPullRequestInNamesOnlyThatRepository(t *testing.T) {
	for _, tc := range []struct {
		link, repo string
		want       int
	}{
		{"https://forgejo/o/r/pulls/3", "o/r", 3},
		{"https://forgejo/O/R/pulls/3", "o/r", 3},
		{"https://forgejo/sub/path/o/r/pulls/3", "o/r", 3},
		{"https://gitlab/g/sub/p/-/merge_requests/4", "g/sub/p", 4},
		{"https://forgejo/other/repo/pulls/123", "o/r", 0},
		{"https://forgejo/xo/r/pulls/3", "o/r", 0},
		{"https://forgejo/o/r/issues/3", "o/r", 0},
		{"https://forgejo/o/r/pulls/3", "", 0},
	} {
		if got := PullRequestIn(tc.link, tc.repo); got != tc.want {
			t.Errorf("PullRequestIn(%q, %q) = %d, want %d", tc.link, tc.repo, got, tc.want)
		}
	}
}

func TestDeliveryMismatch(t *testing.T) {
	head, before := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bind := DeliveryBinding{Forge: "home", Repository: "o/r", Branch: "agent/vik-1", Base: "main"}
	good := Delivery{Forge: "home", Repository: "o/r", Branch: "agent/vik-1", Observed: DeliveryOpened,
		Number: 3, URL: "https://forgejo/o/r/pulls/3", Base: "main", Head: head}
	if why := good.Mismatch(bind); why != "" {
		t.Fatalf("a matching delivery was refused: %s", why)
	}
	updated := good
	updated.Observed, updated.HeadBefore = DeliveryUpdated, before
	if why := updated.Mismatch(bind); why != "" {
		t.Fatalf("a moved head was refused: %s", why)
	}
	unknown := Delivery{Forge: "home", Repository: "o/r", Branch: "agent/vik-1", Observed: DeliveryUnknown, Reason: "503"}
	if why := unknown.Mismatch(bind); why != "" {
		t.Fatalf("an unknown delivery on the right branch was refused: %s", why)
	}
	none := Delivery{Repository: "o/r", Branch: "agent/vik-1", Observed: DeliveryNone}
	if why := none.Mismatch(bind); why != "" {
		t.Fatalf("an empty none was refused: %s", why)
	}
	for name, mutate := range map[string]func(*Delivery){
		"other forge":       func(d *Delivery) { d.Forge = "elsewhere" },
		"other repository":  func(d *Delivery) { d.Repository = "other/repo" },
		"other branch":      func(d *Delivery) { d.Branch = "main" },
		"foreign url":       func(d *Delivery) { d.URL = "https://forgejo/other/repo/pulls/3" },
		"url of another pr": func(d *Delivery) { d.URL = "https://forgejo/o/r/pulls/4" },
		"no number":         func(d *Delivery) { d.Number = 0 },
		"other base":        func(d *Delivery) { d.Base = "release" },
		"no head":           func(d *Delivery) { d.Head = "" },
		"unknown value":     func(d *Delivery) { d.Observed = "merged" },
		"updated, same head": func(d *Delivery) {
			d.Observed, d.HeadBefore = DeliveryUpdated, d.Head
		},
		"opened, no pr": func(d *Delivery) { d.Number, d.URL = 0, "" },
	} {
		d := good
		mutate(&d)
		if why := d.Mismatch(bind); why == "" {
			t.Errorf("%s: Mismatch accepted %+v", name, d)
		}
	}
}

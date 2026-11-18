package team

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewIDIsTeamPrefixAndALowercaseULID(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	id, err := NewID(at, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if !IsID(id) {
		t.Fatalf("%q is not a Team id", id)
	}
	if got, want := id, "team_01hf7yat00zzzzzzzzzzzzzzzz"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
	later, err := NewID(at.Add(time.Millisecond), bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if later[:15] <= id[:15] {
		t.Fatalf("a later id %q does not sort after %q", later, id)
	}
}

func TestNewIDFailsWithoutEntropy(t *testing.T) {
	if _, err := NewID(time.Now(), strings.NewReader("short")); err == nil {
		t.Fatal("an id was made from five bytes of entropy")
	}
}

func TestMustNewIDNeverRepeats(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := MustNewID()
		if seen[id] {
			t.Fatalf("id %q repeated", id)
		}
		seen[id] = true
	}
}

func TestRuntimeKeys(t *testing.T) {
	for _, ok := range []string{"a", "docs-review", "x1", strings.Repeat("a", 40)} {
		if !ValidRuntimeKey(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", strings.Repeat("a", 41), "team_01hf2ws4h0zzzzzzzzzzzzzzzz"} {
		if ValidRuntimeKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"builder · openhands", "a", strings.Repeat("é", 80)} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 81), "a\nb", "a\x00", string([]byte{0xff})} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDefaultNameIsTheRoleAndHarnessOfAOneRoleTeam(t *testing.T) {
	if got := DefaultName("bronze", []RoleHarness{{Role: "builder", Harness: "openhands"}}); got != "builder · openhands" {
		t.Fatalf("one Role: %q", got)
	}
	if got := DefaultName("bronze", []RoleHarness{{Role: "builder", Harness: "openhands"}, {Role: "reviewer", Harness: "claude"}}); got != "bronze" {
		t.Fatalf("two Roles: %q", got)
	}
	if got := DefaultName("bronze", []RoleHarness{{Role: "", Harness: "openhands"}}); got != "bronze" {
		t.Fatalf("plan-less: %q", got)
	}
}

func TestDirectoryResolvesIDKeyAndAlias(t *testing.T) {
	d := NewDirectory([]Team{
		{ID: "team_a", Key: "docs-review", Aliases: []string{"bronze-docs"}},
		{ID: "team_b", Key: "silver"},
	})
	for ref, want := range map[string]Resolution{"team_a": ByID, "docs-review": ByKey, "DOCS-REVIEW": ByKey, "bronze-docs": ByAlias} {
		got, how, ok := d.Resolve(ref)
		if !ok || got.ID != "team_a" || how != want {
			t.Errorf("%q: %+v %v %v", ref, got, how, ok)
		}
	}
	if _, _, ok := d.Resolve("copper"); ok {
		t.Error("an unknown key resolved")
	}
	if d.ID("silver") != "team_b" || d.ID("") != "" {
		t.Error("ID")
	}
	if all := d.All(); len(all) != 2 || all[0].Key != "docs-review" {
		t.Errorf("All = %+v", all)
	}
}

func TestDirectoryPrefersAKeyOverAnotherTeamsAlias(t *testing.T) {
	d := NewDirectory([]Team{{ID: "team_a", Key: "a", Aliases: []string{"b"}}, {ID: "team_b", Key: "b"}})
	if got, how, _ := d.Resolve("b"); got.ID != "team_b" || how != ByKey {
		t.Fatalf("b resolved to %+v by %v", got, how)
	}
}

func TestNilDirectoryResolvesNothing(t *testing.T) {
	var d *Directory
	if _, _, ok := d.Resolve("a"); ok || d.All() != nil {
		t.Fatal("a nil directory resolved")
	}
}

func TestStaticNameSourceNamesTheFileKey(t *testing.T) {
	got := StaticNameSource("bronze", true)
	if got.Source != "configuration" || got.Key != "teams.bronze.name" || !got.Default {
		t.Fatalf("%+v", got)
	}
}

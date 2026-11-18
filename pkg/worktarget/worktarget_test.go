package worktarget

import (
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestRepositoryAndKeyShapes(t *testing.T) {
	for _, ok := range []string{"acme/app", "group/sub/project", "a.b/c-d_e"} {
		if !ValidRepository(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"https://github.com/acme/app", "git@github.com:acme/app.git", "acme", "acme/app.git", "acme/../x", "acme/.hidden", "/acme/app", "acme/app/"} {
		if ValidRepository(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := DeriveKey("github", "Acme/Sub/App"); got != "github:acme:sub:app" {
		t.Errorf("derived key = %q", got)
	}
}

func TestVerdictOffersPrepareOnlyWhenTheInstructionFileIsTheOnlyReason(t *testing.T) {
	at := time.Now()
	if v := Verdict(provider.RepositoryState{Branch: "main"}, "acme/app", at); v.Ready || !v.PrepareAvailable || len(v.Reasons) != 1 {
		t.Errorf("missing instruction file = %+v", v)
	}
	if v := Verdict(provider.RepositoryState{Branch: "main", Archived: true}, "acme/app", at); v.PrepareAvailable {
		t.Errorf("archived and missing = %+v", v)
	}
	if v := Verdict(provider.RepositoryState{Branch: "main", AgentsFile: true}, "acme/app", at); !v.Ready {
		t.Errorf("ready = %+v", v)
	}
}

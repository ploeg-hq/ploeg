package github

import (
	"context"
	"net/http"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestHasWriteAccessCountsWriteAndAdminOnly(t *testing.T) {
	f, p := newFake(t)
	f.json("GET /repos/acme/app/collaborators/maintainer/permission", http.StatusOK, `{"permission":"admin"}`)
	f.json("GET /repos/acme/app/collaborators/writer/permission", http.StatusOK, `{"permission":"write","role_name":"maintain"}`)
	f.json("GET /repos/acme/app/collaborators/triager/permission", http.StatusOK, `{"permission":"read","role_name":"triage"}`)
	f.json("GET /repos/acme/app/collaborators/outsider/permission", http.StatusOK, `{"permission":"none"}`)
	f.json("GET /repos/acme/app/collaborators/ghost/permission", http.StatusNotFound, `{"message":"Not Found"}`)
	f.json("GET /repos/acme/app/collaborators/blocked/permission", http.StatusForbidden, `{"message":"Resource not accessible by integration"}`)
	ctx := context.Background()
	for login, want := range map[string]bool{"maintainer": true, "writer": true, "triager": false, "outsider": false, "ghost": false} {
		got, err := provider.HasWriteAccess(ctx, p, "acme/app", login)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if got != want {
			t.Errorf("%s: write access = %v, want %v", login, got, want)
		}
	}
	if _, err := provider.HasWriteAccess(ctx, p, "acme/app", "blocked"); err == nil {
		t.Error("a permission GitHub refused to show read as an answer")
	}
}

package gitlab

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const noteHook = `{"object_kind":"note","project":{"path_with_namespace":"o/r"},"user":{"username":"ryan"},` +
	`"object_attributes":{"id":555,"note":"please rename","updated_at":"2026-10-28 09:00:00 UTC"},` +
	`"merge_request":{"iid":4,"source_branch":"agent/vik-1","last_commit":{"id":"abc"}}}`

func gitlabHook(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forge/gitlab", strings.NewReader(body))
	r.Header.Set("X-Gitlab-Token", "s3cret")
	return r
}

func TestDeliveryID_UsesGitLabsIdempotencyKey(t *testing.T) {
	r := gitlabHook(noteHook)
	r.Header.Set(IdempotencyKeyHeader, "0b1c")
	if id := (&Provider{Secret: "s3cret"}).DeliveryID(r); id != "0b1c" {
		t.Fatalf("id = %q, want GitLab's Idempotency-Key", id)
	}
}

func TestDeliveryID_IsSyntheticWithoutAnIdempotencyKey(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	first := p.DeliveryID(gitlabHook(noteHook))
	if first == "" || first != p.DeliveryID(gitlabHook(noteHook)) {
		t.Fatalf("a replayed body gave id %q and a different one", first)
	}
	edited := strings.Replace(noteHook, "09:00:00", "09:01:00", 1)
	if first == p.DeliveryID(gitlabHook(edited)) {
		t.Fatal("an edited note shares the id of the original")
	}
}

func TestDeliveryID_AnObjectWithoutATimeIsNotDeduplicated(t *testing.T) {
	body := `{"object_kind":"pipeline","project":{"path_with_namespace":"o/r"},"object_attributes":{"id":9,"status":"failed"}}`
	if id := (&Provider{Secret: "s3cret"}).DeliveryID(gitlabHook(body)); id != "" {
		t.Fatalf("a pipeline without a finished time gave id %q", id)
	}
}

func TestParseWebhook_ANoteCarriesItsIDAsTheReviewID(t *testing.T) {
	p := &Provider{Secret: "s3cret"}
	r := gitlabHook(noteHook)
	if p.DeliveryID(r) == "" {
		t.Fatal("no id")
	}
	events, err := p.ParseWebhook(r)
	if err != nil || len(events) != 1 || events[0].Kind != provider.ForgeReviewSubmitted || events[0].ReviewID != "555" {
		t.Fatalf("events = %+v, %v; want one review with id 555", events, err)
	}
}

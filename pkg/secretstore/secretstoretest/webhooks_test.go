package secretstoretest

import (
	"context"
	"errors"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

var _ provider.WebhookSecretSink = secretstore.WebhookSecrets{}

func TestAWebhookSecretATrackerReturnsOnceIsKeptInTheStore(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	sink := secretstore.WebhookSecrets{Store: fake, Tracker: "clickup"}
	if err := sink.StoreWebhookSecret(ctx, "wh-1", "returned-once"); err != nil {
		t.Fatal(err)
	}
	if got, err := sink.WebhookSecret(ctx, "wh-1"); err != nil || got != "returned-once" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := sink.StoreWebhookSecret(ctx, "../x", "s"); !errors.Is(err, secretstore.ErrOutsidePath) {
		t.Fatalf("outside path = %v", err)
	}
	if err := (secretstore.WebhookSecrets{Store: secretstore.Unconfigured{}, Tracker: "clickup"}).StoreWebhookSecret(ctx, "wh-2", "s"); !errors.Is(err, secretstore.ErrNotConfigured) {
		t.Fatalf("no store = %v", err)
	}
	if err := sink.DeleteWebhookSecret(ctx, "wh-1"); err != nil || fake.Has("webhooks/clickup/wh-1") {
		t.Fatalf("delete = %v", err)
	}
}

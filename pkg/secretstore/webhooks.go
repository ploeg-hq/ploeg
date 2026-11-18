package secretstore

import (
	"context"
	"errors"
	"regexp"
)

// WebhookSecrets keeps the signing secret a tracker returns once when Ploeg
// registers a webhook (ADR-0184 rule 2) under webhooks/<tracker>/<id>. It
// implements provider.WebhookSecretSink without importing it.
type WebhookSecrets struct {
	Store   Store
	Tracker string
}

var webhookID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (w WebhookSecrets) path(id string) (string, error) {
	if !webhookID.MatchString(w.Tracker) || !webhookID.MatchString(id) {
		return "", ErrOutsidePath
	}
	return "webhooks/" + w.Tracker + "/" + id, nil
}

// StoreWebhookSecret writes secret for the webhook id and returns nothing
// but an error; the caller records only the webhook id.
func (w WebhookSecrets) StoreWebhookSecret(ctx context.Context, id, secret string) error {
	if !Configured(w.Store) {
		return ErrNotConfigured
	}
	if secret == "" {
		return errors.New("secret store: empty webhook secret")
	}
	path, err := w.path(id)
	if err != nil {
		return err
	}
	_, err = w.Store.Put(ctx, path, map[string]string{"secret": secret})
	return err
}

// WebhookSecret reads the secret of the webhook id, for verifying its
// deliveries. A webhook's secret is written once, at registration, so it is
// always version 1.
func (w WebhookSecrets) WebhookSecret(ctx context.Context, id string) (string, error) {
	if !Configured(w.Store) {
		return "", ErrNotConfigured
	}
	path, err := w.path(id)
	if err != nil {
		return "", err
	}
	values, err := w.Store.Get(ctx, Ref{Store: w.Store.Name(), Path: path, Version: 1})
	if err != nil {
		return "", err
	}
	return values["secret"], nil
}

// DeleteWebhookSecret removes the secret once the webhook is deleted.
func (w WebhookSecrets) DeleteWebhookSecret(ctx context.Context, id string) error {
	path, err := w.path(id)
	if err != nil {
		return err
	}
	return w.Store.Delete(ctx, path)
}

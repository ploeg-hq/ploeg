package connection

import (
	"context"
	"errors"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// hookSecretMaxAge bounds how long a replica trusts a connection's webhook
// secret it read, so a connection another replica removed stops verifying
// deliveries within it.
const hookSecretMaxAge = 30 * time.Second

type heldHookSecret struct {
	secret string
	held   bool
	at     time.Time
}

// WebhookSecret returns the secret that proves the deliveries of a live
// runtime forge connection's repository webhooks (ADR-0203), read from the
// store. held is false for a connection Ploeg does not hold or that holds no
// webhook secret. An error means Ploeg cannot tell; the delivery must then
// be refused.
func (s *Service) WebhookSecret(ctx context.Context, id string) (secret string, held bool, err error) {
	if !namePattern.MatchString(id) {
		return "", false, nil
	}
	now := s.now()
	s.mu.Lock()
	if h, ok := s.hookSecrets[id]; ok && now.Sub(h.at) < hookSecretMaxAge {
		s.mu.Unlock()
		return h.secret, h.held, nil
	}
	s.mu.Unlock()
	row, err := s.Store.Connection(ctx, s.tenant(), id)
	switch {
	case errors.Is(err, store.ErrConnectionNotFound):
		s.holdHookSecret(id, heldHookSecret{at: now})
		return "", false, nil
	case err != nil:
		return "", false, err
	case row.RemovedAt != nil || row.Port != PortForge:
		s.holdHookSecret(id, heldHookSecret{at: now})
		return "", false, nil
	}
	_, values, err := s.storedCredential(ctx, row)
	if errors.Is(err, secretstore.ErrNotFound) {
		s.holdHookSecret(id, heldHookSecret{at: now})
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	h := heldHookSecret{secret: values["webhook_secret"], held: values["webhook_secret"] != "", at: now}
	s.holdHookSecret(id, h)
	return h.secret, h.held, nil
}

func (s *Service) holdHookSecret(id string, h heldHookSecret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hookSecrets == nil {
		s.hookSecrets = map[string]heldHookSecret{}
	}
	s.hookSecrets[id] = h
}

func (s *Service) forgetHookSecret(id string) {
	s.mu.Lock()
	delete(s.hookSecrets, id)
	s.mu.Unlock()
}

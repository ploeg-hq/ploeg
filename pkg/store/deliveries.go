package store

import (
	"context"
	"time"
)

// DeliveryState says what a webhook handler may do with a delivery it is
// about to act on.
type DeliveryState string

const (
	// DeliveryClaimed means this handler holds the delivery and runs its
	// effects, then calls DeliveryClaim.Complete or DeliveryClaim.Release.
	DeliveryClaimed DeliveryState = "claimed"
	// DeliveryInFlight means another handler holds a live claim on the same
	// delivery and has not finished its effects yet.
	DeliveryInFlight DeliveryState = "in_flight"
	// DeliveryCompleted means the effects of this delivery already committed.
	DeliveryCompleted DeliveryState = "completed"
	// DeliveryUnidentified means the delivery carries no id. Its effects run
	// and nothing is recorded, so a redelivery runs them again.
	DeliveryUnidentified DeliveryState = "unidentified"
)

// DeliveryClaim is one handler's hold on one webhook delivery.
type DeliveryClaim struct {
	State      DeliveryState
	provider   string
	deliveryID string
	token      string
}

// ClaimDelivery takes the delivery for this handler in one statement, so two
// concurrent deliveries of the same id never both act. The claim lasts lease:
// a handler that crashes before Complete leaves a claim that expires, and the
// next delivery of the same id claims it and runs the effects again.
func (s *Store) ClaimDelivery(ctx context.Context, provider, deliveryID string, lease time.Duration) (DeliveryClaim, error) {
	claim := DeliveryClaim{State: DeliveryUnidentified, provider: provider, deliveryID: deliveryID}
	if deliveryID == "" {
		return claim, nil
	}
	token, err := newToken()
	if err != nil {
		return claim, err
	}
	var ours, completed bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO forge_deliveries AS d (provider, delivery_id, claim, claimed_until)
		VALUES ($1, $2, $3, now() + $4::interval)
		ON CONFLICT (provider, delivery_id) DO UPDATE SET
			claim = CASE WHEN d.completed_at IS NULL AND (d.claimed_until IS NULL OR d.claimed_until < now())
				THEN EXCLUDED.claim ELSE d.claim END,
			claimed_until = CASE WHEN d.completed_at IS NULL AND (d.claimed_until IS NULL OR d.claimed_until < now())
				THEN EXCLUDED.claimed_until ELSE d.claimed_until END
		RETURNING d.claim IS NOT DISTINCT FROM $3, d.completed_at IS NOT NULL`,
		provider, deliveryID, token, lease.String()).Scan(&ours, &completed)
	if err != nil {
		return claim, err
	}
	switch {
	case completed:
		claim.State = DeliveryCompleted
	case ours:
		claim.State = DeliveryClaimed
		claim.token = token
	default:
		claim.State = DeliveryInFlight
	}
	return claim, nil
}

// CompleteDelivery records that every effect of the claimed delivery committed, so a
// later delivery of the same id does nothing. It does nothing for a claim
// that is not DeliveryClaimed.
func (s *Store) CompleteDelivery(ctx context.Context, c DeliveryClaim) error {
	if c.State != DeliveryClaimed {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE forge_deliveries SET completed_at = now(), claim = NULL, claimed_until = NULL
		WHERE provider = $1 AND delivery_id = $2 AND claim = $3`, c.provider, c.deliveryID, c.token)
	return err
}

// ReleaseDelivery gives up a claim whose effects failed, so the next delivery
// of the same id acts at once instead of waiting for the lease to expire. It
// does nothing for a claim that is not DeliveryClaimed.
func (s *Store) ReleaseDelivery(ctx context.Context, c DeliveryClaim) error {
	if c.State != DeliveryClaimed {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM forge_deliveries
		WHERE provider = $1 AND delivery_id = $2 AND claim = $3 AND completed_at IS NULL`,
		c.provider, c.deliveryID, c.token)
	return err
}

// Provider returns the dedupe key the claim was taken under.
func (c DeliveryClaim) Provider() string { return c.provider }

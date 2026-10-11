package store

import "context"

// DeliveryRecorded reports whether a delivery id was recorded for provider.
// An empty id is never recorded.
func (s *Store) DeliveryRecorded(ctx context.Context, provider, deliveryID string) (bool, error) {
	if deliveryID == "" {
		return false, nil
	}
	var recorded bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM forge_deliveries WHERE provider = $1 AND delivery_id = $2)`,
		provider, deliveryID).Scan(&recorded)
	return recorded, err
}

// RecordDelivery records a delivery id whose effects all succeeded. Recording
// the same id twice is not an error.
func (s *Store) RecordDelivery(ctx context.Context, provider, deliveryID string) error {
	if deliveryID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO forge_deliveries (provider, delivery_id) VALUES ($1, $2)
		 ON CONFLICT (provider, delivery_id) DO NOTHING`, provider, deliveryID)
	return err
}

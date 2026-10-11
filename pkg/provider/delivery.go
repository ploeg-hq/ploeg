package provider

import "net/http"

// DeliveryIdentifier is implemented by a TrackerProvider whose webhooks carry
// a delivery id that stays the same when the tracker redelivers. Ploeg skips a
// delivery whose effects it already recorded under that id, and records the id
// only after every effect succeeded, so a failed delivery can be replayed.
type DeliveryIdentifier interface {
	DeliveryID(r *http.Request) string
}

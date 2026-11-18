package httpapi

import (
	"net/http"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// AppWebhookReceiver verifies and parses the deliveries of the Apps Ploeg
// holds (ADR-0195), each with that App's own webhook secret.
type AppWebhookReceiver interface {
	// ForApp reports whether a delivery is for an App Ploeg holds. An
	// error means Ploeg cannot tell, and the delivery is refused.
	ForApp(r *http.Request) (bool, error)
	ParseWebhook(r *http.Request) ([]provider.ForgeEvent, error)
}

// handleAppWebhook takes a delivery for an App Ploeg holds on a route with
// no static provider of that kind, or on the tracker route: its grant events
// reach the connections, and every other event is acknowledged and dropped,
// because no static provider is configured to act on it.
func (s *Server) handleAppWebhook(w http.ResponseWriter, r *http.Request, key, name string, app AppWebhookReceiver) {
	events, err := app.ParseWebhook(r)
	if err != nil {
		s.Log.Warn("app webhook rejected", "provider", name, "err", err)
		http.Error(w, "webhook rejected", http.StatusBadRequest)
		return
	}
	var delivery string
	if identifier, ok := app.(provider.DeliveryIdentifier); ok {
		delivery = identifier.DeliveryID(r)
	}
	claim, proceed := s.claimDelivery(w, r, key, delivery)
	if !proceed {
		return
	}
	for _, ev := range events {
		if ev.Kind == provider.ForgeGrantChanged && ev.Grant != nil {
			s.handleGrant(r, name, *ev.Grant)
			continue
		}
		s.Log.Info("app webhook event dropped: no static provider of this kind acts on it", "provider", name, "kind", ev.Kind)
	}
	s.completeDelivery(r, claim)
	w.WriteHeader(http.StatusAccepted)
}

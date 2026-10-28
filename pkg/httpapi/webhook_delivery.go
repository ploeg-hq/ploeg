package httpapi

import (
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const webhookDeliveryLease = 5 * time.Minute

func (s *Server) claimDelivery(w http.ResponseWriter, r *http.Request, key, delivery string) (store.DeliveryClaim, bool) {
	claim, err := s.Store.ClaimDelivery(r.Context(), key, delivery, webhookDeliveryLease)
	if err != nil {
		s.Log.Error("webhook dedup failed", "provider", key, "err", err)
		http.Error(w, "dedup failed", http.StatusInternalServerError)
		return claim, false
	}
	switch claim.State {
	case store.DeliveryCompleted:
		s.Log.Info("webhook redelivered; its effects already committed", "provider", key, "delivery", delivery)
		w.WriteHeader(http.StatusAccepted)
		return claim, false
	case store.DeliveryInFlight:
		s.Log.Info("webhook redelivered while the first delivery is still acting", "provider", key, "delivery", delivery)
		w.Header().Set("Retry-After", "60")
		http.Error(w, "delivery in flight", http.StatusConflict)
		return claim, false
	}
	return claim, true
}

func (s *Server) completeDelivery(r *http.Request, claim store.DeliveryClaim) {
	if err := s.Store.CompleteDelivery(r.Context(), claim); err != nil {
		s.Log.Error("webhook delivery not marked complete; a redelivery after the lease repeats its effects",
			"provider", claim.Provider(), "err", err)
	}
}

func (s *Server) releaseDelivery(r *http.Request, claim store.DeliveryClaim) {
	if err := s.Store.ReleaseDelivery(r.Context(), claim); err != nil {
		s.Log.Error("webhook delivery claim not released; a redelivery acts once the lease expires",
			"provider", claim.Provider(), "err", err)
	}
}

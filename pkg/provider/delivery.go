package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// DeliveryIdentifier is implemented by a TrackerProvider or ForgeProvider
// whose webhooks can be told apart by an id that stays the same when the
// sender redelivers. Ploeg claims a delivery under that id before it acts and
// marks it complete only after every effect committed, so a redelivery of a
// completed delivery does nothing and a delivery that failed or crashed part
// way acts again. ploegd calls DeliveryID after ParseWebhook accepted the
// request.
type DeliveryIdentifier interface {
	DeliveryID(r *http.Request) string
}

// SyntheticDeliveryID derives a delivery id from fields of a signed payload,
// for a sender that puts no delivery id on its webhooks. The fields name the
// provider, the event kind, the object and when the object changed. It
// returns "" when any field is empty, because an id built without the change
// time would treat two real changes as one delivery and drop the second.
func SyntheticDeliveryID(fields ...string) string {
	for _, f := range fields {
		if strings.TrimSpace(f) == "" {
			return ""
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return "synthetic:" + hex.EncodeToString(sum[:])
}

// ReadWebhookBody reads at most limit bytes of the request body and puts
// them back, so ParseWebhook and DeliveryID can both read the same payload.
func ReadWebhookBody(r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

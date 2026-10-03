package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
)

const testTrackerSecret = "tracker-fixture-secret"

var trackerSignatureHeader = map[string]string{"vikunja": "X-Vikunja-Signature", "clickup": "X-Signature"}

func signedTrackerHook(dialect, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/"+dialect, strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(testTrackerSecret))
	mac.Write([]byte(body))
	r.Header.Set(trackerSignatureHeader[dialect], hex.EncodeToString(mac.Sum(nil)))
	return r
}

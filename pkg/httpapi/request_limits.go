package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	claimBodyLimit      int64 = 64 << 10
	checkpointBodyLimit int64 = 1 << 20
	outcomeBodyLimit    int64 = 8 << 20

	contextTransferBudget = 5 * time.Minute
)

func decodeBounded(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(out)
}

func refuseOversizedBody(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		return false
	}
	http.Error(w, fmt.Sprintf("request body exceeds %d bytes", tooLarge.Limit), http.StatusRequestEntityTooLarge)
	return true
}

func extendTransferDeadline(w http.ResponseWriter, budget time.Duration) {
	deadline := time.Now().Add(budget)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

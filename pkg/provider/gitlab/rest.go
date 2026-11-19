package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

// restError is a GitLab answer outside 2xx. Message is GitLab's own reason,
// which never carries the token that was sent.
type restError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *restError) Error() string {
	return fmt.Sprintf("gitlab: %s %s answered %d: %s", e.Method, e.Path, e.Status, e.Message)
}

func (e *restError) Unwrap() error {
	switch {
	case e.Status == http.StatusUnauthorized:
		return provider.ErrCredentialRefused
	case e.Status >= 500 || e.Status == http.StatusTooManyRequests:
		return provider.ErrVendorUnavailable
	}
	return provider.ErrServiceAccountRefused
}

func statusOf(err error) int {
	var re *restError
	if errors.As(err, &re) {
		return re.Status
	}
	return 0
}

// rest sends one JSON request to GitLab's API with token as a Bearer
// credential and decodes a 2xx answer into out.
func rest(ctx context.Context, method, rawURL, token string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return fmt.Errorf("gitlab request: %w", provider.ErrVendorUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := oauthconn.Client.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab unreachable: %w", provider.ErrVendorUnavailable)
	}
	defer resp.Body.Close()
	path := req.URL.Path
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &restError{Method: method, Path: path, Status: resp.StatusCode, Message: reasonOf(snippet)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("gitlab: %s %s: unreadable answer: %w", method, path, provider.ErrVendorUnavailable)
	}
	return nil
}

func reasonOf(body []byte) string {
	var shaped struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &shaped) == nil {
		switch m := shaped.Message.(type) {
		case string:
			return m
		case nil:
		default:
			raw, _ := json.Marshal(m)
			return string(raw)
		}
		if shaped.Error != "" {
			return shaped.Error
		}
	}
	return strings.TrimSpace(string(body))
}

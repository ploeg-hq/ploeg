// Package oauthconn is the OAuth 2.0 authorization-code plumbing the
// tracker and forge connectors share (ADR-0195): building an authorize URL,
// posting a token request and reading the vendor's account with a bearer.
// Errors never carry a request or response body, so no code, secret or token
// reaches a log through them.
package oauthconn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/httpclient"
	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Client is the HTTP client connectors use; tests replace it.
var Client = httpclient.New(30 * time.Second)

// Challenge is the S256 PKCE challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL appends the authorization-code parameters to base.
func AuthorizeURL(base string, params map[string]string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("oauth: invalid authorize URL")
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// Token posts form to tokenURL and reads an OAuth token response. A 400 or
// 401 with an error is ErrCredentialRefused; a 5xx or transport failure is
// ErrVendorUnavailable.
func Token(ctx context.Context, tokenURL string, form url.Values, now time.Time) (provider.OAuthCredential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return provider.OAuthCredential{}, fmt.Errorf("oauth: token request: %w", provider.ErrVendorUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := Client.Do(req)
	if err != nil {
		return provider.OAuthCredential{}, fmt.Errorf("oauth: token endpoint unreachable: %w", provider.ErrVendorUnavailable)
	}
	defer resp.Body.Close()
	var body tokenResponse
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	switch {
	case resp.StatusCode >= 500:
		return provider.OAuthCredential{}, fmt.Errorf("oauth: token endpoint answered %d: %w", resp.StatusCode, provider.ErrVendorUnavailable)
	case resp.StatusCode >= 400:
		return provider.OAuthCredential{}, fmt.Errorf("oauth: token endpoint answered %d: %w", resp.StatusCode, provider.ErrCredentialRefused)
	case decodeErr != nil || body.AccessToken == "" || body.Error != "":
		return provider.OAuthCredential{}, fmt.Errorf("oauth: token endpoint gave no access token: %w", provider.ErrCredentialRefused)
	}
	cred := provider.OAuthCredential{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, Scopes: strings.FieldsFunc(body.Scope, func(r rune) bool { return r == ' ' || r == ',' })}
	if body.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return cred, nil
}

// GetJSON reads url with header as Authorization into out. A 401 or 403 is
// ErrCredentialRefused; a 5xx or transport failure is ErrVendorUnavailable.
func GetJSON(ctx context.Context, rawURL, authorization string, out any) error {
	return Do(ctx, http.MethodGet, rawURL, authorization, nil, out)
}

// Do sends one JSON request with authorization and decodes a JSON answer
// into out when out is not nil.
func Do(ctx context.Context, method, rawURL, authorization string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return fmt.Errorf("vendor request: %w", provider.ErrVendorUnavailable)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := Client.Do(req)
	if err != nil {
		return fmt.Errorf("vendor unreachable: %w", provider.ErrVendorUnavailable)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s %s answered %d: %w", method, stripQuery(rawURL), resp.StatusCode, provider.ErrCredentialRefused)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s answered 404: %w", method, stripQuery(rawURL), provider.ErrCredentialRefused)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s %s answered %d: %w", method, stripQuery(rawURL), resp.StatusCode, provider.ErrVendorUnavailable)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s %s: unreadable answer: %w", method, stripQuery(rawURL), provider.ErrVendorUnavailable)
	}
	return nil
}

func stripQuery(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// Trim returns base without a trailing slash.
func Trim(base string) string { return strings.TrimRight(base, "/") }

// Missing returns the entries of need absent from have.
func Missing(need, have []string) []string {
	held := map[string]bool{}
	for _, s := range have {
		held[s] = true
	}
	var out []string
	for _, s := range need {
		if !held[s] {
			out = append(out, s)
		}
	}
	return out
}

// PostForm posts form to rawURL and only checks the status.
func PostForm(ctx context.Context, rawURL string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("vendor request: %w", provider.ErrVendorUnavailable)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := Client.Do(req)
	if err != nil {
		return fmt.Errorf("vendor unreachable: %w", provider.ErrVendorUnavailable)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode >= 500:
		return fmt.Errorf("POST %s answered %d: %w", stripQuery(rawURL), resp.StatusCode, provider.ErrVendorUnavailable)
	case resp.StatusCode >= 400:
		return fmt.Errorf("POST %s answered %d: %w", stripQuery(rawURL), resp.StatusCode, provider.ErrCredentialRefused)
	}
	return nil
}

// Lookup reads rawURL like GetJSON, except that a 404 is found false with
// no error, so a caller can tell a missing resource from a refused
// credential.
func Lookup(ctx context.Context, rawURL, authorization string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, fmt.Errorf("vendor request: %w", provider.ErrVendorUnavailable)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := Client.Do(req)
	if err != nil {
		return false, fmt.Errorf("vendor unreachable: %w", provider.ErrVendorUnavailable)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return false, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("GET %s answered %d: %w", stripQuery(rawURL), resp.StatusCode, provider.ErrCredentialRefused)
	case resp.StatusCode >= 300:
		return false, fmt.Errorf("GET %s answered %d: %w", stripQuery(rawURL), resp.StatusCode, provider.ErrVendorUnavailable)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return true, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return false, fmt.Errorf("GET %s: unreadable answer: %w", stripQuery(rawURL), provider.ErrVendorUnavailable)
	}
	return true, nil
}

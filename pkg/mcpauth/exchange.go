package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token types of RFC 8693 section 3.
const (
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
	grantTokenExchange   = "urn:ietf:params:oauth:grant-type:token-exchange"
)

// ErrExchangeRefused means the identity provider answered but issued no
// token, for example because the subject token expired or the client may not
// exchange for that audience.
var ErrExchangeRefused = errors.New("the identity provider refused the token exchange")

// Exchanger trades a person's verified access token for one issued for
// another audience on that person's behalf (RFC 8693 token exchange). It
// authenticates as its own confidential client with HTTP Basic.
type Exchanger struct {
	tokenURL     string
	clientID     string
	clientSecret string
	audience     string
	scope        string
	client       *http.Client
}

// ExchangeConfig pins one Exchanger.
type ExchangeConfig struct {
	// TokenURL is the identity provider's token endpoint. It must be https
	// unless AllowLoopbackHTTP is set and its host is loopback.
	TokenURL string
	// AllowLoopbackHTTP accepts http on a loopback host, as for Config. It is
	// for local development and tests only and is outside profile P1.
	AllowLoopbackHTTP bool
	// ClientID and ClientSecret are this service's own client credentials.
	ClientID     string
	ClientSecret string
	// Audience is the audience the exchanged token is issued for.
	Audience string
	// Scope, when set, is sent as the requested scope.
	Scope string
	// HTTPClient nil means a client with a five second timeout that follows
	// no redirects.
	HTTPClient *http.Client
}

// NewExchanger validates cfg.
func NewExchanger(cfg ExchangeConfig) (*Exchanger, error) {
	u, err := url.Parse(cfg.TokenURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("token endpoint must be an https URL without credentials or fragment")
	}
	if !transportAllowed(u, cfg.AllowLoopbackHTTP) {
		return nil, errors.New(httpRefused("token endpoint"))
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || strings.ContainsAny(cfg.ClientID, ":\r\n") {
		return nil, errors.New("token exchange needs this service's client id (without a colon) and client secret")
	}
	if cfg.Audience == "" || strings.ContainsAny(cfg.Audience, " \t\r\n") {
		return nil, errors.New("token exchange needs one audience")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Exchanger{tokenURL: cfg.TokenURL, clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, audience: cfg.Audience, scope: cfg.Scope, client: cfg.HTTPClient}, nil
}

// Audience is the audience exchanged tokens are issued for.
func (e *Exchanger) Audience() string { return e.audience }

// Exchange returns an access token for the configured audience on behalf of
// the person subjectToken belongs to. It never returns or logs subjectToken.
func (e *Exchanger) Exchange(ctx context.Context, subjectToken string) (string, error) {
	form := url.Values{
		"grant_type":           {grantTokenExchange},
		"subject_token":        {subjectToken},
		"subject_token_type":   {TokenTypeAccessToken},
		"requested_token_type": {TokenTypeAccessToken},
		"audience":             {e.audience},
	}
	if e.scope != "" {
		form.Set("scope", e.scope)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("could not build the token exchange request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(e.clientID), url.QueryEscape(e.clientSecret))
	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the identity provider's token endpoint is unreachable: %w", ctx.Err())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil || len(body) > maxDocumentBytes {
		return "", errors.New("the token endpoint's answer could not be read")
	}
	var answer struct {
		AccessToken     string `json:"access_token"`
		IssuedTokenType string `json:"issued_token_type"`
		Error           string `json:"error"`
	}
	_ = json.Unmarshal(body, &answer)
	if resp.StatusCode != http.StatusOK {
		code := answer.Error
		if code == "" || len(code) > 64 || strings.ContainsFunc(code, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			code = "unknown"
		}
		return "", fmt.Errorf("%w (%d %s)", ErrExchangeRefused, resp.StatusCode, code)
	}
	if answer.AccessToken == "" || strings.ContainsAny(answer.AccessToken, " \t\r\n,") {
		return "", fmt.Errorf("%w: the answer holds no usable access_token", ErrExchangeRefused)
	}
	if answer.IssuedTokenType != "" && answer.IssuedTokenType != TokenTypeAccessToken && answer.IssuedTokenType != "urn:ietf:params:oauth:token-type:jwt" {
		return "", fmt.Errorf("%w: issued token type %q is not an access token", ErrExchangeRefused, answer.IssuedTokenType)
	}
	return answer.AccessToken, nil
}

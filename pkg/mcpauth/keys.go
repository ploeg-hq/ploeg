package mcpauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDocumentBytes = 1 << 20
	keySetMaxAge     = time.Hour
	unknownKeyPause  = 30 * time.Second
	failedFetchPause = 2 * time.Second
)

// ErrKeysUnavailable means the issuer's signing keys could not be fetched, so
// no token can be judged either way.
var ErrKeysUnavailable = errors.New("the issuer's signing keys are unavailable")

type keySet struct {
	issuer  string
	jwksURL string
	client  *http.Client
	now     func() time.Time

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
	triedAt   time.Time

	loaded  atomic.Bool
	loadMu  sync.Mutex
	loading chan struct{}
}

func newKeySet(issuer, jwksURL string, client *http.Client, now func() time.Time) *keySet {
	return &keySet{issuer: issuer, jwksURL: jwksURL, client: client, now: now}
}

func (s *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	stale := s.keys == nil || now.Sub(s.fetchedAt) > keySetMaxAge
	_, known := s.lookup(kid)
	pause := unknownKeyPause
	if s.keys == nil {
		pause = failedFetchPause
	}
	if (stale || !known) && (s.triedAt.IsZero() || now.Sub(s.triedAt) >= pause) {
		s.triedAt = now
		if keys, err := s.fetch(ctx); err == nil {
			s.keys, s.fetchedAt = keys, now
			s.loaded.Store(true)
		} else if s.keys == nil {
			return nil, fmt.Errorf("%w: %v", ErrKeysUnavailable, err)
		}
	}
	if s.keys == nil {
		return nil, ErrKeysUnavailable
	}
	if key, ok := s.lookup(kid); ok {
		return key, nil
	}
	return nil, errors.New("the token's key id is not among the issuer's signing keys")
}

// ready reports whether the keys are loaded. A fetch it starts runs to its
// own timeout even when ctx ends first, so a readiness probe shorter than the
// issuer's answer still loads the keys for the next probe.
func (s *keySet) ready(ctx context.Context) error {
	if s.loaded.Load() {
		return nil
	}
	s.loadMu.Lock()
	if s.loading == nil {
		done := make(chan struct{})
		s.loading = done
		go func() {
			_, _ = s.key(context.WithoutCancel(ctx), "")
			s.loadMu.Lock()
			s.loading = nil
			s.loadMu.Unlock()
			close(done)
		}()
	}
	done := s.loading
	s.loadMu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrKeysUnavailable, ctx.Err())
	}
	if !s.loaded.Load() {
		return ErrKeysUnavailable
	}
	return nil
}

func (s *keySet) lookup(kid string) (crypto.PublicKey, bool) {
	if kid == "" {
		if len(s.keys) != 1 {
			return nil, false
		}
		for _, k := range s.keys {
			return k, true
		}
	}
	k, ok := s.keys[kid]
	return k, ok
}

func (s *keySet) fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	jwksURL := s.jwksURL
	if jwksURL == "" {
		var discovery struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := s.getJSON(ctx, strings.TrimRight(s.issuer, "/")+"/.well-known/openid-configuration", &discovery); err != nil {
			return nil, err
		}
		if discovery.Issuer != s.issuer {
			return nil, fmt.Errorf("discovery names issuer %q, not the configured %q", discovery.Issuer, s.issuer)
		}
		if !strings.HasPrefix(discovery.JWKSURI, "https://") && !strings.HasPrefix(discovery.JWKSURI, "http://") {
			return nil, errors.New("discovery has no http(s) jwks_uri")
		}
		jwksURL = discovery.JWKSURI
	}
	var doc struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := s.getJSON(ctx, jwksURL, &doc); err != nil {
		return nil, err
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("the key set holds no usable signing key")
	}
	return keys, nil
}

func (s *keySet) getJSON(ctx context.Context, url string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxDocumentBytes {
		return fmt.Errorf("%s answered more than 1 MiB", url)
	}
	return json.Unmarshal(body, into)
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jsonWebKey) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := decodeInt(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeInt(k.E)
		if err != nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("invalid RSA exponent")
		}
		if n.BitLen() < 2048 {
			return nil, errors.New("RSA key shorter than 2048 bits")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err := decodeInt(k.X)
		if err != nil {
			return nil, err
		}
		y, err := decodeInt(k.Y)
		if err != nil {
			return nil, err
		}
		size := (curve.Params().BitSize + 7) / 8
		if x.BitLen() > 8*size || y.BitLen() > 8*size {
			return nil, errors.New("EC coordinate longer than the curve")
		}
		raw := make([]byte, 1+2*size)
		raw[0] = 4
		x.FillBytes(raw[1 : 1+size])
		y.FillBytes(raw[1+size:])
		return ecdsa.ParseUncompressedPublicKey(curve, raw)
	}
	return nil, errors.New("unsupported key type")
}

func decodeInt(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("invalid base64url integer")
	}
	return new(big.Int).SetBytes(raw), nil
}

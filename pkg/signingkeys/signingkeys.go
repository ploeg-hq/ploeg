// Package signingkeys reads the P-256 keys ploegd signs its JWTs with and
// publishes their public halves as a JWK Set (RFC 7517), the current key
// first and then the retired ones, so a token signed just before a rotation
// still verifies.
package signingkeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Algorithm is the JOSE algorithm of every key this package reads.
const Algorithm = "ES256"

// ParsePrivateKey reads exactly one PEM P-256 private key, PKCS#8 or SEC 1.
func ParsePrivateKey(privatePEM string) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(privatePEM))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the signing key must be exactly one PEM block")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("signing key: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("the signing key must be an ECDSA P-256 key")
		}
		key = ec
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("signing key: %w", err)
		}
		key = k
	default:
		return nil, fmt.Errorf("the signing key must be a PEM PRIVATE KEY or EC PRIVATE KEY, not %s", block.Type)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("the signing key must be a P-256 key")
	}
	return key, nil
}

// ParsePublicKeys reads concatenated PEM "PUBLIC KEY" blocks of P-256 keys;
// empty text is no keys.
func ParsePublicKeys(text string) ([]*ecdsa.PublicKey, error) {
	var keys []*ecdsa.PublicKey
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("a retired key must be a PEM PUBLIC KEY, not %s", block.Type)
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("retired key: %w", err)
		}
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok || ec.Curve != elliptic.P256() {
			return nil, errors.New("a retired key must be a P-256 key")
		}
		keys = append(keys, ec)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the retired keys hold text that is not PEM")
	}
	return keys, nil
}

// JWK is one public key of a JWK Set (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// JWKS is a JWK Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// Set is the JWK Set of current followed by retired.
func Set(current *ecdsa.PublicKey, retired []*ecdsa.PublicKey) JWKS {
	set := JWKS{Keys: []JWK{PublicJWK(current)}}
	for _, k := range retired {
		set.Keys = append(set.Keys, PublicJWK(k))
	}
	return set
}

// PublicJWK is k as a signing JWK whose kid is its RFC 7638 thumbprint.
func PublicJWK(k *ecdsa.PublicKey) JWK {
	x, y := coordinates(k)
	return JWK{Kty: "EC", Crv: "P-256", X: x, Y: y, Kid: Thumbprint(k), Use: "sig", Alg: Algorithm}
}

func coordinates(k *ecdsa.PublicKey) (string, string) {
	raw, _ := k.Bytes()
	return base64.RawURLEncoding.EncodeToString(raw[1:33]), base64.RawURLEncoding.EncodeToString(raw[33:65])
}

// Thumbprint is the RFC 7638 JWK thumbprint of a P-256 key.
func Thumbprint(k *ecdsa.PublicKey) string {
	x, y := coordinates(k)
	sum := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ParseKeySet reads a JWK Set and keeps its P-256 signing keys by kid.
func ParseKeySet(body []byte) (map[string]*ecdsa.PublicKey, error) {
	var set JWKS
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("key set: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != Algorithm) || k.Kid == "" {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("the key set holds no P-256 signing key")
	}
	return keys, nil
}

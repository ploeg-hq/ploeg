package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"testing"
)

func TestRunIdentitySignerIsOffWithoutAKeyAndRefusesHalfAConfiguration(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	log := slog.New(slog.DiscardHandler)
	allowlist := `{"gold":{"builder":["https://link.example"]}}`
	for name, tc := range map[string]struct {
		key, issuer, audiences string
		on, fails, allowsLink  bool
	}{
		"off":                   {},
		"on without audiences":  {key: priv, issuer: "https://ploeg.example", on: true},
		"on with audiences":     {key: priv, issuer: "https://ploeg.example", audiences: allowlist, on: true, allowsLink: true},
		"issuer without key":    {issuer: "https://ploeg.example", fails: true},
		"key without issuer":    {key: priv, fails: true},
		"audiences without key": {audiences: allowlist, fails: true},
		"malformed audiences":   {key: priv, issuer: "https://ploeg.example", audiences: `{"gold":`, fails: true},
		"plain http audience":   {key: priv, issuer: "https://ploeg.example", audiences: `{"gold":{"builder":["http://link.example"]}}`, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PLOEG_RUN_IDENTITY_SIGNING_KEY", tc.key)
			t.Setenv("PLOEG_RUN_IDENTITY_ISSUER", tc.issuer)
			t.Setenv("PLOEG_RUN_IDENTITY_RETIRED_KEYS", "")
			t.Setenv("PLOEG_RUN_IDENTITY_AUDIENCES", tc.audiences)
			s, audiences, err := runIdentitySigner(log)
			if (err != nil) != tc.fails || (s != nil) != tc.on {
				t.Errorf("signer %v, err %v", s, err)
			}
			if got := audiences.Allows("gold", "builder", "https://link.example"); got != tc.allowsLink {
				t.Errorf("allows the listed audience = %v, want %v", got, tc.allowsLink)
			}
		})
	}
}

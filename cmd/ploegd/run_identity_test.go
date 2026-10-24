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
	for name, tc := range map[string]struct {
		key, issuer string
		on, fails   bool
	}{
		"off":                {},
		"on":                 {key: priv, issuer: "https://ploeg.example", on: true},
		"issuer without key": {issuer: "https://ploeg.example", fails: true},
		"key without issuer": {key: priv, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PLOEG_RUN_IDENTITY_SIGNING_KEY", tc.key)
			t.Setenv("PLOEG_RUN_IDENTITY_ISSUER", tc.issuer)
			t.Setenv("PLOEG_RUN_IDENTITY_RETIRED_KEYS", "")
			s, err := runIdentitySigner(log)
			if (err != nil) != tc.fails || (s != nil) != tc.on {
				t.Errorf("signer %v, err %v", s, err)
			}
		})
	}
}

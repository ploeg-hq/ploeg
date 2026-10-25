package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
)

func transactionTokenKey(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestTransactionTokenSignerIsOffWithoutAKeyAndRefusesHalfAConfiguration(t *testing.T) {
	priv := transactionTokenKey(t)
	log := slog.New(slog.DiscardHandler)
	for name, tc := range map[string]struct {
		key, issuer, audience string
		on, fails             bool
	}{
		"off":                  {},
		"on":                   {key: priv, issuer: "https://ploeg.example", on: true},
		"on with a domain":     {key: priv, issuer: "https://ploeg.example", audience: "urn:ploeg:example", on: true},
		"issuer without key":   {issuer: "https://ploeg.example", fails: true},
		"audience without key": {audience: "urn:ploeg:example", fails: true},
		"key without issuer":   {key: priv, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PLOEG_TRANSACTION_TOKEN_SIGNING_KEY", tc.key)
			t.Setenv("PLOEG_TRANSACTION_TOKEN_ISSUER", tc.issuer)
			t.Setenv("PLOEG_TRANSACTION_TOKEN_AUDIENCE", tc.audience)
			t.Setenv("PLOEG_TRANSACTION_TOKEN_RETIRED_KEYS", "")
			s, err := transactionTokenSigner(log)
			if (err != nil) != tc.fails || (s != nil) != tc.on {
				t.Errorf("signer %v, err %v", s, err)
			}
		})
	}
}

func TestTheHMACCapabilityIsIssuedByDefaultAndRetiresOnlyWithTransactionTokens(t *testing.T) {
	t.Setenv("PLOEG_TRANSACTION_TOKEN_SIGNING_KEY", transactionTokenKey(t))
	t.Setenv("PLOEG_TRANSACTION_TOKEN_ISSUER", "https://ploeg.example")
	log := slog.New(slog.DiscardHandler)
	signer, err := transactionTokenSigner(log)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal([]httpapi.WorkerBootstrap{{Token: strings.Repeat("b", 32), Team: "bronze", Role: "builder"}})
	t.Setenv("PLOEG_WORKER_BOOTSTRAPS", string(registry))
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "managed")
	for name, tc := range map[string]struct {
		mode, hmacKey string
		withSigner    bool
		retired       bool
		fails         bool
	}{
		"default issues":            {hmacKey: strings.Repeat("s", 32), withSigner: true},
		"issue issues":              {mode: "issue", hmacKey: strings.Repeat("s", 32)},
		"off retires":               {mode: "off", withSigner: true, retired: true},
		"off without a signer":      {mode: "off", fails: true},
		"off with a stale HMAC key": {mode: "off", hmacKey: strings.Repeat("s", 32), withSigner: true, fails: true},
		"an unknown mode":           {mode: "maybe", withSigner: true, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PLOEG_WORKER_HMAC_CAPABILITY", tc.mode)
			t.Setenv("PLOEG_WORKER_SIGNING_KEY", tc.hmacKey)
			s := signer
			if !tc.withSigner {
				s = nil
			}
			a, err := workerSecurityFromEnv(s, log)
			if (err != nil) != tc.fails {
				t.Fatalf("err %v", err)
			}
			if err == nil && a.RetireHMAC != tc.retired {
				t.Fatalf("RetireHMAC %v", a.RetireHMAC)
			}
		})
	}
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	t.Setenv("PLOEG_WORKER_HMAC_CAPABILITY", "off")
	t.Setenv("PLOEG_WORKER_SIGNING_KEY", "")
	t.Setenv("PLOEG_WORKER_BOOTSTRAPS", "")
	if _, err := workerSecurityFromEnv(signer, log); err == nil {
		t.Fatal("legacy mode retired the HMAC capability")
	}
}

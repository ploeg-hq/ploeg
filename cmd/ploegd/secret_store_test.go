package main

import (
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

func TestSecretStoreUnsetFailsClosedAndLeavesStaticConfigAlone(t *testing.T) {
	s, err := secretStore(slog.New(slog.DiscardHandler), secretStoreEnv{})
	if err != nil || secretstore.Configured(s) {
		t.Fatalf("store = %v, %v", s, err)
	}
}

func TestSecretStoreRefusesAnUnknownOrIncompleteStore(t *testing.T) {
	for _, env := range []secretStoreEnv{
		{kind: "vault"},
		{kind: "openbao"},
		{kind: "openbao", address: "https://bao:8200"},
	} {
		if _, err := secretStore(slog.New(slog.DiscardHandler), env); err == nil {
			t.Fatalf("env %+v accepted", env)
		}
	}
}

func TestSecretStoreOpenBao(t *testing.T) {
	s, err := secretStore(slog.New(slog.DiscardHandler), secretStoreEnv{kind: "openbao", address: "https://bao:8200", role: "ploeg"})
	if err != nil || s.Name() != "openbao" {
		t.Fatalf("store = %v, %v", s, err)
	}
}

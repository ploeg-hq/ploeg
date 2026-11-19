package main

import (
	"slices"
	"testing"
)

func TestASessionGetsOnlyTheVariablesTheBridgePasses(t *testing.T) {
	environ := []string{"PATH=/usr/bin", "GRAFANA_API_KEY=k", "KUBERNETES_SERVICE_HOST=10.0.0.1", "OTHER_TOKEN=t", "HOME=/"}
	b, listen, err := newBridge([]string{"--listen=:9090", "--sessions-dir=/sessions", "--pass-env=GRAFANA_API_KEY", "--", "/bin/server", "--stdio"}, environ)
	if err != nil {
		t.Fatal(err)
	}
	if listen != ":9090" || b.Root != "/sessions" || !slices.Equal(b.Command, []string{"/bin/server", "--stdio"}) {
		t.Fatalf("bridge = %+v on %s", b, listen)
	}
	if !slices.Equal(b.Env, []string{"PATH=/usr/bin", "GRAFANA_API_KEY=k"}) {
		t.Fatalf("a session's environment = %v", b.Env)
	}
	if _, _, err := newBridge([]string{"--pass-env=A=B", "--", "/bin/server"}, environ); err == nil {
		t.Fatal("a --pass-env that is not a name was accepted")
	}
}

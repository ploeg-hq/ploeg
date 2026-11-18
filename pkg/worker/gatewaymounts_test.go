package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAGatewayWorkerRefusesAMountedServiceAccountToken(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := refuseServiceAccountToken(token); err != nil {
		t.Fatalf("no token mounted, refused: %v", err)
	}
	if err := os.WriteFile(token, []byte("a-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refuseServiceAccountToken(token); err == nil {
		t.Fatal("a mounted ServiceAccount token was accepted")
	}
}

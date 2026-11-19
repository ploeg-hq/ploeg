package launch

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
)

const (
	listenAddress = "127.0.0.1:0"
	tokenBytes    = 32
)

// Rendezvous is what the worker publishes in the volume it shares with the
// harness container once it listens: the loopback address it accepts the
// launcher on and the token the launcher must present in its hello.
type Rendezvous struct {
	Address string `json:"address"`
	Token   string `json:"token"`
}

var errNotLoopback = errors.New("the launcher rendezvous names an address outside the pod's loopback")

func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("draw the launcher token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func tokenMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func publishRendezvous(path string, r Rendezvous) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rendezvous-*")
	if err != nil {
		return fmt.Errorf("publish the launcher rendezvous in %s: %w", filepath.Dir(path), err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o444); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("publish the launcher rendezvous %s: %w", path, err)
	}
	return nil
}

func readRendezvous(path string) (Rendezvous, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Rendezvous{}, err
	}
	var r Rendezvous
	if err := json.Unmarshal(raw, &r); err != nil {
		return Rendezvous{}, fmt.Errorf("read the launcher rendezvous %s: %w", path, err)
	}
	if r.Token == "" {
		return Rendezvous{}, fmt.Errorf("the launcher rendezvous %s carries no token", path)
	}
	address, err := netip.ParseAddrPort(r.Address)
	if err != nil || !address.Addr().IsLoopback() {
		return Rendezvous{}, fmt.Errorf("%w: %q", errNotLoopback, r.Address)
	}
	return r, nil
}

func listenOnLoopback() (net.Listener, error) {
	return net.Listen("tcp", listenAddress)
}

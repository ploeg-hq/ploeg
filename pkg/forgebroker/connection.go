package forgebroker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ConnectionIDPrefix marks a credential minted through a runtime forge
// connection, so its revocation reaches the same connection's broker.
const ConnectionIDPrefix = "connection:"

// ErrConnectionCannotMint is a forge connection whose kind or method mints
// no per-Run credential.
var ErrConnectionCannotMint = errors.New("forgebroker: the forge connection cannot mint a per-run credential")

// ConnectionResolver builds the broker that mints through one runtime forge
// connection (ADR-0193 section 4, ADR-0192): never the static broker of the
// connection's forge.
type ConnectionResolver func(ctx context.Context, connection string) (Broker, error)

// WithConnections sends the mint of a Run whose Work Target names a forge
// connection to that connection's broker and every other mint to Base. A
// mint for a connection is never sent to Base, and Base nil mints nothing
// for a static target, as a release without a broker does.
type WithConnections struct {
	Base        Broker
	BaseSweeper Sweeper
	Resolve     ConnectionResolver

	mu      sync.Mutex
	brokers map[string]Broker
}

func (w *WithConnections) broker(ctx context.Context, connection string) (Broker, error) {
	w.mu.Lock()
	b, ok := w.brokers[connection]
	w.mu.Unlock()
	if ok {
		return b, nil
	}
	if w.Resolve == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoBroker, connection)
	}
	b, err := w.Resolve(ctx, connection)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if w.brokers == nil {
		w.brokers = map[string]Broker{}
	}
	if held, ok := w.brokers[connection]; ok {
		b = held
	} else {
		w.brokers[connection] = b
	}
	w.mu.Unlock()
	return b, nil
}

// Forget drops the broker held for connection, as after it was reconnected
// or removed, so the next mint reads its credential again.
func (w *WithConnections) Forget(connection string) {
	w.mu.Lock()
	delete(w.brokers, connection)
	w.mu.Unlock()
}

// Mint implements Broker.
func (w *WithConnections) Mint(ctx context.Context, req MintRequest) (Credential, error) {
	if req.Connection == "" {
		if w.Base == nil {
			return Credential{}, nil
		}
		return w.Base.Mint(ctx, req)
	}
	b, err := w.broker(ctx, req.Connection)
	if err != nil {
		return Credential{}, err
	}
	cred, err := b.Mint(ctx, req)
	if err != nil {
		return Credential{}, err
	}
	if cred.ID != "" {
		cred.ID = ConnectionIDPrefix + req.Connection + "|" + cred.ID
	}
	return cred, nil
}

func splitConnectionID(id string) (connection, inner string, ok bool) {
	rest, ok := strings.CutPrefix(id, ConnectionIDPrefix)
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "|")
}

// Revoke implements Broker.
func (w *WithConnections) Revoke(ctx context.Context, cred Credential) error {
	if connection, inner, ok := splitConnectionID(cred.ID); ok {
		w.mu.Lock()
		b, held := w.brokers[connection]
		w.mu.Unlock()
		if !held {
			return nil
		}
		cred.ID = inner
		return b.Revoke(ctx, cred)
	}
	if w.Base == nil {
		return nil
	}
	return w.Base.Revoke(ctx, cred)
}

// RevokeByID implements Sweeper.
func (w *WithConnections) RevokeByID(ctx context.Context, id string) error {
	if _, _, ok := splitConnectionID(id); ok {
		return w.Revoke(ctx, Credential{ID: id})
	}
	if w.BaseSweeper == nil {
		return nil
	}
	return w.BaseSweeper.RevokeByID(ctx, id)
}

// SweepOrphans implements Sweeper over Base and every connection broker
// that sweeps.
func (w *WithConnections) SweepOrphans(ctx context.Context, leased func(context.Context) ([]string, error)) (int, error) {
	total := 0
	var errs []error
	if w.BaseSweeper != nil {
		n, err := w.BaseSweeper.SweepOrphans(ctx, leased)
		total += n
		errs = append(errs, err)
	}
	w.mu.Lock()
	brokers := make([]Broker, 0, len(w.brokers))
	for _, b := range w.brokers {
		brokers = append(brokers, b)
	}
	w.mu.Unlock()
	for _, b := range brokers {
		if s, ok := b.(Sweeper); ok {
			n, err := s.SweepOrphans(ctx, leased)
			total += n
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// Package secretstore is the SecretStore port of ADR-0184: Ploeg writes the
// secrets of the tracker and forge connections it makes into a store and
// keeps only a reference (store, path, version) in its own records. A value
// never reaches Ploeg's database, logs, API responses or events.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNotFound is returned when nothing is stored at a reference.
	ErrNotFound = errors.New("secret store: not found")
	// ErrOutsidePath is returned for a path that leaves the adapter's own
	// prefix, or one the adapter did not create.
	ErrOutsidePath = errors.New("secret store: path outside Ploeg's own")
	// ErrUnavailable is returned when the store cannot be reached or refuses
	// Ploeg's credential.
	ErrUnavailable = errors.New("secret store: unavailable")
	// ErrNotConfigured is returned by every call when no store is
	// configured, so a store-backed call fails closed (ADR-0184 rule 5).
	ErrNotConfigured = errors.New("secret store: none configured")
)

// Ref names one version of a stored secret. It is safe to write to a
// database row, a log line, an API response or an event.
type Ref struct {
	Store   string `json:"store"`
	Path    string `json:"path"`
	Version int    `json:"version"`
}

func (r Ref) IsZero() bool { return r.Store == "" && r.Path == "" && r.Version == 0 }

// Entry is one path a List returns.
type Entry struct {
	Path      string
	UpdatedAt time.Time
}

// Store is the SecretStore port. Paths are relative to the adapter's own
// prefix; an adapter refuses any path that would leave it with
// ErrOutsidePath. Values are a small map of named secrets kept together
// under one path (an access token and its refresh token, an App's private
// key and webhook secret).
type Store interface {
	// Name is the adapter's name as a Ref carries it.
	Name() string
	// Put writes values as a new version at path and returns its reference.
	Put(ctx context.Context, path string, values map[string]string) (Ref, error)
	// Get reads the values at ref. A version newer than the store holds,
	// or a deleted path, is ErrNotFound.
	Get(ctx context.Context, ref Ref) (map[string]string, error)
	// Delete removes every version at path. A missing path is not an error.
	Delete(ctx context.Context, path string) error
	// List returns the paths under prefix ("" lists every path Ploeg
	// holds), so a sweep can delete what no record references.
	List(ctx context.Context, prefix string) ([]Entry, error)
}

var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// CheckPath refuses a path that is empty, absolute, longer than 512
// characters or that has a segment outside [A-Za-z0-9_.-] (so "..", "."
// and empty segments are refused).
func CheckPath(path string) error {
	if path == "" || len(path) > 512 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("%w: %q", ErrOutsidePath, path)
	}
	for _, s := range strings.Split(path, "/") {
		if !segment.MatchString(s) || s == "." || s == ".." || strings.Contains(s, "..") {
			return fmt.Errorf("%w: %q", ErrOutsidePath, path)
		}
	}
	return nil
}

// CheckValues refuses an empty map, an empty name or more than 16 values,
// and values over 64 KiB in total.
func CheckValues(values map[string]string) error {
	if len(values) == 0 || len(values) > 16 {
		return errors.New("secret store: 1 to 16 values per path")
	}
	total := 0
	for name, value := range values {
		if !segment.MatchString(name) {
			return fmt.Errorf("secret store: invalid value name %q", name)
		}
		total += len(name) + len(value)
	}
	if total > 64*1024 {
		return errors.New("secret store: values exceed 64 KiB")
	}
	return nil
}

// Unconfigured is the Store an installation without a secret store gets:
// every call fails closed with ErrNotConfigured and nothing is written.
type Unconfigured struct{}

func (Unconfigured) Name() string { return "" }
func (Unconfigured) Put(context.Context, string, map[string]string) (Ref, error) {
	return Ref{}, ErrNotConfigured
}
func (Unconfigured) Get(context.Context, Ref) (map[string]string, error) {
	return nil, ErrNotConfigured
}
func (Unconfigured) Delete(context.Context, string) error { return ErrNotConfigured }
func (Unconfigured) List(context.Context, string) ([]Entry, error) {
	return nil, ErrNotConfigured
}

// Configured reports whether s is a usable store.
func Configured(s Store) bool {
	if s == nil {
		return false
	}
	_, unconfigured := s.(Unconfigured)
	return !unconfigured
}

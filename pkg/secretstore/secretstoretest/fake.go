// Package secretstoretest holds an in-memory SecretStore for tests and the
// conformance suite every SecretStore adapter passes (ADR-0184).
package secretstoretest

import (
	"context"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// Fake is an in-memory, versioned SecretStore. Fail makes every call
// answer ErrUnavailable until it is cleared.
type Fake struct {
	mu    sync.Mutex
	paths map[string][]map[string]string
	at    map[string]time.Time
	Fail  bool
	Puts  int
}

func NewFake() *Fake {
	return &Fake{paths: map[string][]map[string]string{}, at: map[string]time.Time{}}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Put(_ context.Context, path string, values map[string]string) (secretstore.Ref, error) {
	if err := secretstore.CheckPath(path); err != nil {
		return secretstore.Ref{}, err
	}
	if err := secretstore.CheckValues(values); err != nil {
		return secretstore.Ref{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail {
		return secretstore.Ref{}, secretstore.ErrUnavailable
	}
	f.Puts++
	f.paths[path] = append(f.paths[path], maps.Clone(values))
	f.at[path] = time.Now()
	return secretstore.Ref{Store: f.Name(), Path: path, Version: len(f.paths[path])}, nil
}

func (f *Fake) Get(_ context.Context, ref secretstore.Ref) (map[string]string, error) {
	if err := secretstore.CheckPath(ref.Path); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail {
		return nil, secretstore.ErrUnavailable
	}
	versions := f.paths[ref.Path]
	if ref.Version < 1 || ref.Version > len(versions) {
		return nil, secretstore.ErrNotFound
	}
	return maps.Clone(versions[ref.Version-1]), nil
}

func (f *Fake) Delete(_ context.Context, path string) error {
	if err := secretstore.CheckPath(path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail {
		return secretstore.ErrUnavailable
	}
	delete(f.paths, path)
	delete(f.at, path)
	return nil
}

func (f *Fake) List(_ context.Context, prefix string) ([]secretstore.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail {
		return nil, secretstore.ErrUnavailable
	}
	var out []secretstore.Entry
	for path := range f.paths {
		if strings.HasPrefix(path, prefix) {
			out = append(out, secretstore.Entry{Path: path, UpdatedAt: f.at[path]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Age sets when path was last written, so a sweep test can make it old.
func (f *Fake) Age(path string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at[path] = at
}

// Values returns every value the fake holds, for redaction tests.
func (f *Fake) Values() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, versions := range f.paths {
		for _, v := range versions {
			for _, value := range v {
				out = append(out, value)
			}
		}
	}
	return out
}

// Has reports whether anything is stored at path.
func (f *Fake) Has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths[path]) > 0
}

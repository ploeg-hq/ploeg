package secretstoretest

import (
	"context"
	"errors"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// Run is the SecretStore conformance suite: put, get, overwrite, delete,
// list, and a path outside the adapter's own being refused. newStore
// returns a fresh, empty store for each case.
func Run(t *testing.T, newStore func(t *testing.T) secretstore.Store) {
	t.Helper()
	ctx := context.Background()
	t.Run("put then get", func(t *testing.T) {
		s := newStore(t)
		ref, err := s.Put(ctx, "connections/a", map[string]string{"access_token": "one"})
		if err != nil {
			t.Fatal(err)
		}
		if ref.Store != s.Name() || ref.Path != "connections/a" || ref.Version < 1 {
			t.Fatalf("ref = %+v", ref)
		}
		got, err := s.Get(ctx, ref)
		if err != nil || got["access_token"] != "one" {
			t.Fatalf("get = %v, %v", got, err)
		}
	})
	t.Run("overwrite makes a new version", func(t *testing.T) {
		s := newStore(t)
		first, err := s.Put(ctx, "connections/b", map[string]string{"access_token": "one"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.Put(ctx, "connections/b", map[string]string{"access_token": "two", "refresh_token": "r"})
		if err != nil {
			t.Fatal(err)
		}
		if second.Version <= first.Version || second.Path != first.Path {
			t.Fatalf("first %+v second %+v", first, second)
		}
		got, err := s.Get(ctx, second)
		if err != nil || got["access_token"] != "two" || got["refresh_token"] != "r" {
			t.Fatalf("get = %v, %v", got, err)
		}
	})
	t.Run("delete then get is not found", func(t *testing.T) {
		s := newStore(t)
		ref, err := s.Put(ctx, "connections/c", map[string]string{"k": "v"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "connections/c"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, ref); !errors.Is(err, secretstore.ErrNotFound) {
			t.Fatalf("get after delete = %v", err)
		}
		if err := s.Delete(ctx, "connections/c"); err != nil {
			t.Fatalf("second delete = %v", err)
		}
	})
	t.Run("missing is not found", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get(ctx, secretstore.Ref{Store: s.Name(), Path: "connections/none", Version: 1}); !errors.Is(err, secretstore.ErrNotFound) {
			t.Fatalf("get missing = %v", err)
		}
	})
	t.Run("list", func(t *testing.T) {
		s := newStore(t)
		for _, p := range []string{"connections/x", "flows/y"} {
			if _, err := s.Put(ctx, p, map[string]string{"k": "v"}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx, "connections/")
		if err != nil || len(got) != 1 || got[0].Path != "connections/x" {
			t.Fatalf("list = %+v, %v", got, err)
		}
	})
	t.Run("outside its own path is refused", func(t *testing.T) {
		s := newStore(t)
		for _, p := range []string{"../other", "/abs", "a/../../b", "", "a//b"} {
			if _, err := s.Put(ctx, p, map[string]string{"k": "v"}); !errors.Is(err, secretstore.ErrOutsidePath) {
				t.Fatalf("put %q = %v", p, err)
			}
			if _, err := s.Get(ctx, secretstore.Ref{Store: s.Name(), Path: p, Version: 1}); !errors.Is(err, secretstore.ErrOutsidePath) {
				t.Fatalf("get %q = %v", p, err)
			}
			if err := s.Delete(ctx, p); !errors.Is(err, secretstore.ErrOutsidePath) {
				t.Fatalf("delete %q = %v", p, err)
			}
		}
	})
}

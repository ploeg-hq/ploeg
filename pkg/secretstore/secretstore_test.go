package secretstore

import (
	"context"
	"errors"
	"testing"
)

func TestUnconfiguredFailsClosed(t *testing.T) {
	var s Store = Unconfigured{}
	if Configured(s) || Configured(nil) {
		t.Fatal("an unconfigured store reports configured")
	}
	if _, err := s.Put(context.Background(), "a", map[string]string{"k": "v"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("put = %v", err)
	}
	if _, err := s.Get(context.Background(), Ref{Path: "a", Version: 1}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("get = %v", err)
	}
	if err := s.Delete(context.Background(), "a"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("delete = %v", err)
	}
}

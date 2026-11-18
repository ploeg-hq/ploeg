package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
)

type fakeAPI struct {
	mu      sync.Mutex
	secrets map[string]object
	rv      int
	down    bool
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer sa-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	const base = "/api/v1/namespaces/ploeg/secrets"
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, base), "/")
	switch {
	case r.Method == http.MethodGet && name == "":
		selector := r.URL.Query().Get("labelSelector")
		var items []object
		for _, obj := range f.secrets {
			match := true
			for _, pair := range strings.Split(selector, ",") {
				k, v, _ := strings.Cut(pair, "=")
				if obj.Metadata.Labels[k] != v {
					match = false
				}
			}
			if match {
				items = append(items, obj)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodGet:
		obj, ok := f.secrets[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(obj)
	case r.Method == http.MethodPost:
		var obj object
		_ = json.NewDecoder(r.Body).Decode(&obj)
		if _, exists := f.secrets[obj.Metadata.Name]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.rv++
		obj.Metadata.ResourceVersion = strconv.Itoa(f.rv)
		f.secrets[obj.Metadata.Name] = obj
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut:
		var obj object
		_ = json.NewDecoder(r.Body).Decode(&obj)
		if f.secrets[name].Metadata.ResourceVersion != obj.Metadata.ResourceVersion {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.rv++
		obj.Metadata.ResourceVersion = strconv.Itoa(f.rv)
		f.secrets[name] = obj
		_ = json.NewEncoder(w).Encode(obj)
	case r.Method == http.MethodDelete:
		delete(f.secrets, name)
		w.WriteHeader(http.StatusOK)
	}
}

func newStore(t *testing.T, fake *fakeAPI) *Store {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Namespace: "ploeg", APIURL: server.URL, TokenFile: tokenFile, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestKubernetesConforms(t *testing.T) {
	secretstoretest.Run(t, func(t *testing.T) secretstore.Store { return newStore(t, &fakeAPI{secrets: map[string]object{}}) })
}

func TestKubernetesRefusesASecretItDidNotCreate(t *testing.T) {
	fake := &fakeAPI{secrets: map[string]object{}}
	s := newStore(t, fake)
	name := SecretName("connections/a")
	fake.secrets[name] = object{Metadata: metadata{Name: name, Annotations: map[string]string{PathAnnotation: "connections/a", VersionAnnotation: "1"}}, Data: map[string][]byte{"k": []byte("theirs")}}
	ctx := context.Background()
	if _, err := s.Get(ctx, secretstore.Ref{Path: "connections/a", Version: 1}); !errors.Is(err, secretstore.ErrOutsidePath) {
		t.Fatalf("get = %v", err)
	}
	if _, err := s.Put(ctx, "connections/a", map[string]string{"k": "ours"}); !errors.Is(err, secretstore.ErrOutsidePath) {
		t.Fatalf("put = %v", err)
	}
	if err := s.Delete(ctx, "connections/a"); !errors.Is(err, secretstore.ErrOutsidePath) {
		t.Fatalf("delete = %v", err)
	}
	if string(fake.secrets[name].Data["k"]) != "theirs" {
		t.Fatal("the foreign Secret changed")
	}
	if got, err := s.List(ctx, ""); err != nil || len(got) != 0 {
		t.Fatalf("list = %v, %v", got, err)
	}
}

func TestKubernetesUnavailable(t *testing.T) {
	fake := &fakeAPI{secrets: map[string]object{}, down: true}
	s := newStore(t, fake)
	if _, err := s.Put(context.Background(), "a", map[string]string{"k": "v"}); !errors.Is(err, secretstore.ErrUnavailable) {
		t.Fatalf("put = %v", err)
	}
}

func TestSecretNameIsDNSLabel(t *testing.T) {
	name := SecretName("connections/" + strings.Repeat("x", 300))
	if len(name) > 63 || !namespacePattern.MatchString(name) {
		t.Fatalf("name %q", name)
	}
}

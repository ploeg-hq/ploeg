package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
)

type fakeBao struct {
	mu       sync.Mutex
	prefix   string
	tokens   map[string]bool
	logins   int
	versions map[string][]map[string]string
	down     bool
}

func newFakeBao(prefix string) *fakeBao {
	return &fakeBao{prefix: prefix, tokens: map[string]bool{}, versions: map[string][]map[string]string{}}
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/v1/auth/kubernetes/login" {
		var in struct{ Role, JWT string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Role != "ploeg" || in.JWT != "sa-token" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.logins++
		token := "t" + strconv.Itoa(f.logins)
		f.tokens[token] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": 3600}})
		return
	}
	if !f.tokens[r.Header.Get("X-Vault-Token")] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	dataPrefix, metaPrefix := "/v1/secret/data/"+f.prefix+"/", "/v1/secret/metadata/"+f.prefix+"/"
	switch {
	case strings.HasPrefix(r.URL.Path, dataPrefix):
		path := strings.TrimPrefix(r.URL.Path, dataPrefix)
		switch r.Method {
		case http.MethodPost:
			var in struct {
				Data map[string]string `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.versions[path] = append(f.versions[path], in.Data)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": len(f.versions[path])}})
		case http.MethodGet:
			v, _ := strconv.Atoi(r.URL.Query().Get("version"))
			versions := f.versions[path]
			if v < 1 || v > len(versions) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": versions[v-1], "metadata": map[string]any{"deletion_time": "", "destroyed": false}}})
		}
	case strings.HasPrefix(r.URL.Path, metaPrefix) || r.URL.Path+"/" == metaPrefix:
		path := strings.TrimPrefix(r.URL.Path, metaPrefix)
		switch r.Method {
		case http.MethodDelete:
			delete(f.versions, path)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if _, ok := f.versions[path]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"updated_time": time.Now().UTC()}})
		case "LIST":
			keys := map[string]bool{}
			for p := range f.versions {
				if rest, ok := strings.CutPrefix(p, path); ok && rest != "" {
					if head, _, nested := strings.Cut(rest, "/"); nested {
						keys[head+"/"] = true
					} else {
						keys[rest] = true
					}
				}
			}
			if len(keys) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var list []string
			for k := range keys {
				list = append(list, k)
			}
			sort.Strings(list)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": list}})
		}
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func newStore(t *testing.T, fake *fakeBao) *Store {
	t.Helper()
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Address: server.URL, Role: "ploeg", TokenFile: tokenFile, PathPrefix: fake.prefix, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpenBaoConforms(t *testing.T) {
	secretstoretest.Run(t, func(t *testing.T) secretstore.Store { return newStore(t, newFakeBao("ploeg")) })
}

func TestOpenBaoSignsInAgainWhenItsTokenIsRefused(t *testing.T) {
	fake := newFakeBao("ploeg")
	s := newStore(t, fake)
	ctx := context.Background()
	if _, err := s.Put(ctx, "a", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.tokens = map[string]bool{}
	fake.mu.Unlock()
	if _, err := s.Put(ctx, "a", map[string]string{"k": "w"}); err != nil {
		t.Fatalf("put after revoked token = %v", err)
	}
	if fake.logins != 2 {
		t.Fatalf("logins = %d, want 2", fake.logins)
	}
}

func TestOpenBaoWritesOnlyUnderItsPrefix(t *testing.T) {
	fake := newFakeBao("ploeg")
	s := newStore(t, fake)
	if _, err := s.Put(context.Background(), "connections/x", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.versions["connections/x"]; !ok {
		t.Fatalf("written paths = %v", fake.versions)
	}
}

func TestOpenBaoUnavailable(t *testing.T) {
	fake := newFakeBao("ploeg")
	s := newStore(t, fake)
	fake.down = true
	if _, err := s.Put(context.Background(), "a", map[string]string{"k": "secret-value"}); !errors.Is(err, secretstore.ErrUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("put while down = %v", err)
	}
}

func TestOpenBaoConfigRefused(t *testing.T) {
	for _, cfg := range []Config{{Address: "bao:8200", Role: "r"}, {Address: "https://bao", Role: ""}, {Address: "https://bao", Role: "r", PathPrefix: "../x"},
		{Address: "http://bao:8200", Role: "r"}} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
}

func TestOpenBaoAcceptsHTTPOnlyWhenAskedExplicitly(t *testing.T) {
	if _, err := New(Config{Address: "http://bao:8200", Role: "r", AllowInsecureHTTP: true}); err != nil {
		t.Fatalf("explicit insecure http = %v", err)
	}
}

func TestOpenBaoNeverFollowsARedirectWithItsTokens(t *testing.T) {
	var leaked []string
	var mu sync.Mutex
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("X-Vault-Token"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)
	fake := newFakeBao("ploeg")
	bao := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			fake.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(bao.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := bao.Client()
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(elsewhere.Certificate())
	s, err := New(Config{Address: bao.URL, Role: "ploeg", TokenFile: tokenFile, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), "a", map[string]string{"k": "v"}); err == nil {
		t.Fatal("a redirected write succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Fatalf("the client followed a redirect and sent %d requests elsewhere", len(leaked))
	}
}

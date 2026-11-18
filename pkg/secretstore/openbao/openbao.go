// Package openbao is the OpenBao and Vault KV v2 adapter of the SecretStore
// port (ADR-0184). It signs in with Kubernetes auth under a role whose
// policy allows Ploeg only its own path prefix, and keeps every value under
// that prefix.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// DefaultTokenFile is where a pod's projected service account token lives.
const DefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Config configures the adapter.
type Config struct {
	// Address is the OpenBao or Vault base URL, such as https://bao:8200.
	Address string
	// AuthMount is the Kubernetes auth mount; empty is "kubernetes".
	AuthMount string
	// Role is the Kubernetes auth role Ploeg signs in as.
	Role string
	// KVMount is the KV v2 mount; empty is "secret".
	KVMount string
	// PathPrefix is the path under KVMount that holds everything Ploeg
	// writes; empty is "ploeg".
	PathPrefix string
	// Namespace is sent as X-Vault-Namespace when set.
	Namespace string
	// TokenFile is the service account token; empty is DefaultTokenFile.
	TokenFile string
	// CAFile, when set, is the PEM bundle that verifies Address.
	CAFile string
	// AllowInsecureHTTP permits an http:// Address. Without it only https
	// is accepted, because every request carries Ploeg's token or its
	// service account token.
	AllowInsecureHTTP bool
	// HTTPClient replaces the default client, for tests. Redirects are
	// never followed, whichever client is used.
	HTTPClient *http.Client
	// Now replaces time.Now, for tests.
	Now func() time.Time
}

// Store is the adapter.
type Store struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates cfg and returns the adapter. It does not sign in until the
// first call.
func New(cfg Config) (*Store, error) {
	u, err := url.Parse(cfg.Address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("openbao: address must be an https URL")
	}
	if u.Scheme == "http" && !cfg.AllowInsecureHTTP {
		return nil, errors.New("openbao: address must be an https URL; an http address needs the explicit insecure setting")
	}
	cfg.Address = strings.TrimRight(cfg.Address, "/")
	if cfg.Role == "" {
		return nil, errors.New("openbao: a Kubernetes auth role is required")
	}
	if cfg.AuthMount == "" {
		cfg.AuthMount = "kubernetes"
	}
	if cfg.KVMount == "" {
		cfg.KVMount = "secret"
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "ploeg"
	}
	for _, p := range []string{cfg.AuthMount, cfg.KVMount, cfg.PathPrefix} {
		if err := secretstore.CheckPath(p); err != nil {
			return nil, fmt.Errorf("openbao: invalid mount or prefix %q", p)
		}
	}
	if cfg.TokenFile == "" {
		cfg.TokenFile = DefaultTokenFile
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		client = &copied
	} else {
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("openbao: read CA file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("openbao: CA file holds no certificate")
			}
			client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
		}
	}
	client.CheckRedirect = refuseRedirect
	return &Store{cfg: cfg, client: client}, nil
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func (s *Store) Name() string { return "openbao" }

func (s *Store) Put(ctx context.Context, path string, values map[string]string) (secretstore.Ref, error) {
	if err := secretstore.CheckPath(path); err != nil {
		return secretstore.Ref{}, err
	}
	if err := secretstore.CheckValues(values); err != nil {
		return secretstore.Ref{}, err
	}
	var out struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	status, err := s.call(ctx, http.MethodPost, s.kv("data", path), map[string]any{"data": values}, &out)
	if err != nil {
		return secretstore.Ref{}, err
	}
	if status != http.StatusOK || out.Data.Version < 1 {
		return secretstore.Ref{}, fmt.Errorf("%w: write answered %d", secretstore.ErrUnavailable, status)
	}
	return secretstore.Ref{Store: s.Name(), Path: path, Version: out.Data.Version}, nil
}

func (s *Store) Get(ctx context.Context, ref secretstore.Ref) (map[string]string, error) {
	if err := secretstore.CheckPath(ref.Path); err != nil {
		return nil, err
	}
	if ref.Version < 1 {
		return nil, secretstore.ErrNotFound
	}
	var out struct {
		Data struct {
			Data     map[string]string `json:"data"`
			Metadata struct {
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"metadata"`
		} `json:"data"`
	}
	status, err := s.call(ctx, http.MethodGet, s.kv("data", ref.Path)+"?version="+strconv.Itoa(ref.Version), nil, &out)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, secretstore.ErrNotFound
	case status != http.StatusOK:
		return nil, fmt.Errorf("%w: read answered %d", secretstore.ErrUnavailable, status)
	case out.Data.Metadata.Destroyed || out.Data.Metadata.DeletionTime != "" || out.Data.Data == nil:
		return nil, secretstore.ErrNotFound
	}
	return out.Data.Data, nil
}

func (s *Store) Delete(ctx context.Context, path string) error {
	if err := secretstore.CheckPath(path); err != nil {
		return err
	}
	status, err := s.call(ctx, http.MethodDelete, s.kv("metadata", path), nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("%w: delete answered %d", secretstore.ErrUnavailable, status)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]secretstore.Entry, error) {
	var out []secretstore.Entry
	if err := s.list(ctx, "", prefix, &out, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) list(ctx context.Context, dir, prefix string, out *[]secretstore.Entry, depth int) error {
	if depth > 8 || len(*out) > 10000 {
		return nil
	}
	var body struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	target := strings.TrimSuffix(s.kv("metadata", strings.TrimSuffix(dir, "/")), "/") + "/"
	if dir == "" {
		target = s.cfg.Address + "/v1/" + s.cfg.KVMount + "/metadata/" + s.cfg.PathPrefix + "/"
	}
	status, err := s.call(ctx, "LIST", target, nil, &body)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status != http.StatusOK {
		return fmt.Errorf("%w: list answered %d", secretstore.ErrUnavailable, status)
	}
	for _, key := range body.Data.Keys {
		full := dir + key
		if strings.HasSuffix(key, "/") {
			if strings.HasPrefix(full, prefix) || strings.HasPrefix(prefix, full) {
				if err := s.list(ctx, full, prefix, out, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		if strings.HasPrefix(full, prefix) && secretstore.CheckPath(full) == nil {
			*out = append(*out, secretstore.Entry{Path: full, UpdatedAt: s.updated(ctx, full)})
		}
	}
	return nil
}

func (s *Store) updated(ctx context.Context, path string) time.Time {
	var body struct {
		Data struct {
			UpdatedTime time.Time `json:"updated_time"`
		} `json:"data"`
	}
	if status, err := s.call(ctx, http.MethodGet, s.kv("metadata", path), nil, &body); err != nil || status != http.StatusOK {
		return time.Time{}
	}
	return body.Data.UpdatedTime
}

func (s *Store) kv(kind, path string) string {
	return s.cfg.Address + "/v1/" + s.cfg.KVMount + "/" + kind + "/" + s.cfg.PathPrefix + "/" + path
}

func (s *Store) call(ctx context.Context, method, target string, in, out any) (int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := s.login(ctx, attempt > 0)
		if err != nil {
			return 0, err
		}
		status, err := s.do(ctx, method, target, token, in, out)
		if err != nil {
			return 0, err
		}
		if status == http.StatusForbidden && attempt == 0 {
			continue
		}
		if status == http.StatusForbidden || status >= 500 {
			return 0, fmt.Errorf("%w: %s answered %d", secretstore.ErrUnavailable, method, status)
		}
		return status, nil
	}
	return 0, secretstore.ErrUnavailable
}

func (s *Store) login(ctx context.Context, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.token != "" && s.cfg.Now().Before(s.expires) {
		return s.token, nil
	}
	jwt, err := os.ReadFile(s.cfg.TokenFile)
	if err != nil {
		return "", fmt.Errorf("%w: read service account token: %v", secretstore.ErrUnavailable, err)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	status, err := s.do(ctx, http.MethodPost, s.cfg.Address+"/v1/auth/"+s.cfg.AuthMount+"/login", "",
		map[string]string{"role": s.cfg.Role, "jwt": strings.TrimSpace(string(jwt))}, &out)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("%w: kubernetes auth answered %d", secretstore.ErrUnavailable, status)
	}
	lease := time.Duration(out.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	s.token, s.expires = out.Auth.ClientToken, s.cfg.Now().Add(lease*9/10)
	return s.token, nil
}

func (s *Store) do(ctx context.Context, method, target, token string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if s.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", s.cfg.Namespace)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %s %s: transport error", secretstore.ErrUnavailable, method, redactPath(target))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("%w: read response", secretstore.ErrUnavailable)
	}
	if out != nil && resp.StatusCode == http.StatusOK && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, fmt.Errorf("%w: malformed response", secretstore.ErrUnavailable)
		}
	}
	return resp.StatusCode, nil
}

func redactPath(target string) string {
	if u, err := url.Parse(target); err == nil {
		return u.Host
	}
	return ""
}

// Package kubernetes is the Kubernetes Secret adapter of the SecretStore
// port (ADR-0184), for installations without a vault. It writes one Secret
// per path in one namespace, labels each as Ploeg's, and refuses to read,
// change or delete a Secret it did not create. A Secret keeps only its
// latest version: a reference to an older version still reads the latest
// values, and a reference to a newer one is not found.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

const (
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	// ManagedByLabel and ManagedBy mark a Secret as created by Ploeg.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedBy      = "ploeg"
	// StoreLabel marks a Secret as one of the SecretStore's own.
	StoreLabel = "ploeg.dev/secret-store"
	// StoreValue is StoreLabel's value.
	StoreValue = "connection"
	// PathAnnotation holds the store path the Secret belongs to.
	PathAnnotation = "ploeg.dev/secret-path"
	// VersionAnnotation holds the Secret's version counter.
	VersionAnnotation = "ploeg.dev/secret-version"
	namePrefix        = "ploeg-secret-"
)

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Config configures the adapter. Empty fields come from the pod's service
// account and environment.
type Config struct {
	// Namespace holds the Secrets; empty is the pod's own namespace.
	Namespace string
	// APIURL is the API server; empty is https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT.
	APIURL string
	// TokenFile is the bearer token; empty is the service account token.
	TokenFile string
	// HTTPClient replaces the in-cluster client, for tests.
	HTTPClient *http.Client
}

// Store is the adapter.
type Store struct {
	namespace, api, tokenFile string
	client                    *http.Client
}

// New returns the adapter, reading the in-cluster defaults it needs.
func New(cfg Config) (*Store, error) {
	s := &Store{namespace: cfg.Namespace, api: strings.TrimRight(cfg.APIURL, "/"), tokenFile: cfg.TokenFile, client: cfg.HTTPClient}
	if s.tokenFile == "" {
		s.tokenFile = serviceAccountDir + "/token"
	}
	if s.namespace == "" {
		raw, err := os.ReadFile(serviceAccountDir + "/namespace")
		if err != nil {
			return nil, errors.New("kubernetes secret store: no namespace configured and the pod's namespace is unreadable")
		}
		s.namespace = strings.TrimSpace(string(raw))
	}
	if !namespacePattern.MatchString(s.namespace) {
		return nil, fmt.Errorf("kubernetes secret store: invalid namespace %q", s.namespace)
	}
	if s.api == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, errors.New("kubernetes secret store: not running in a cluster (KUBERNETES_SERVICE_HOST unset)")
		}
		s.api = "https://" + net.JoinHostPort(host, port)
	}
	if s.client == nil {
		pem, err := os.ReadFile(serviceAccountDir + "/ca.crt")
		if err != nil {
			return nil, fmt.Errorf("kubernetes secret store: read cluster CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("kubernetes secret store: cluster CA holds no certificate")
		}
		s.client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	return s, nil
}

func (s *Store) Name() string { return "kubernetes" }

// SecretName is the Secret that holds path.
func SecretName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return namePrefix + hex.EncodeToString(sum[:])[:40]
}

type object struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metadata          `json:"metadata"`
	Type       string            `json:"type,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type metadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	CreationTimestamp time.Time         `json:"creationTimestamp,omitempty"`
}

func (s *Store) Put(ctx context.Context, path string, values map[string]string) (secretstore.Ref, error) {
	if err := secretstore.CheckPath(path); err != nil {
		return secretstore.Ref{}, err
	}
	if err := secretstore.CheckValues(values); err != nil {
		return secretstore.Ref{}, err
	}
	data := make(map[string][]byte, len(values))
	for k, v := range values {
		data[k] = []byte(v)
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, err := s.read(ctx, path)
		switch {
		case errors.Is(err, secretstore.ErrNotFound):
			obj := s.object(path, 1, data, "")
			status, err := s.call(ctx, http.MethodPost, s.collection(), obj, nil)
			if err != nil {
				return secretstore.Ref{}, err
			}
			if status == http.StatusConflict {
				continue
			}
			if status != http.StatusCreated && status != http.StatusOK {
				return secretstore.Ref{}, fmt.Errorf("%w: create answered %d", secretstore.ErrUnavailable, status)
			}
			return secretstore.Ref{Store: s.Name(), Path: path, Version: 1}, nil
		case err != nil:
			return secretstore.Ref{}, err
		}
		version := versionOf(current) + 1
		obj := s.object(path, version, data, current.Metadata.ResourceVersion)
		status, err := s.call(ctx, http.MethodPut, s.collection()+"/"+SecretName(path), obj, nil)
		if err != nil {
			return secretstore.Ref{}, err
		}
		if status == http.StatusConflict {
			continue
		}
		if status != http.StatusOK {
			return secretstore.Ref{}, fmt.Errorf("%w: update answered %d", secretstore.ErrUnavailable, status)
		}
		return secretstore.Ref{Store: s.Name(), Path: path, Version: version}, nil
	}
	return secretstore.Ref{}, fmt.Errorf("%w: concurrent writes to one path", secretstore.ErrUnavailable)
}

func (s *Store) Get(ctx context.Context, ref secretstore.Ref) (map[string]string, error) {
	if err := secretstore.CheckPath(ref.Path); err != nil {
		return nil, err
	}
	obj, err := s.read(ctx, ref.Path)
	if err != nil {
		return nil, err
	}
	if ref.Version < 1 || ref.Version > versionOf(obj) {
		return nil, secretstore.ErrNotFound
	}
	out := make(map[string]string, len(obj.Data))
	for k, v := range obj.Data {
		out[k] = string(v)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, path string) error {
	if err := secretstore.CheckPath(path); err != nil {
		return err
	}
	if _, err := s.read(ctx, path); errors.Is(err, secretstore.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	status, err := s.call(ctx, http.MethodDelete, s.collection()+"/"+SecretName(path), nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNotFound {
		return fmt.Errorf("%w: delete answered %d", secretstore.ErrUnavailable, status)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]secretstore.Entry, error) {
	var list struct {
		Items []object `json:"items"`
	}
	selector := url.QueryEscape(ManagedByLabel + "=" + ManagedBy + "," + StoreLabel + "=" + StoreValue)
	status, err := s.call(ctx, http.MethodGet, s.collection()+"?labelSelector="+selector, nil, &list)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: list answered %d", secretstore.ErrUnavailable, status)
	}
	var out []secretstore.Entry
	for _, item := range list.Items {
		path := item.Metadata.Annotations[PathAnnotation]
		if owned(item, path) && strings.HasPrefix(path, prefix) {
			out = append(out, secretstore.Entry{Path: path, UpdatedAt: item.Metadata.CreationTimestamp})
		}
	}
	return out, nil
}

func (s *Store) read(ctx context.Context, path string) (object, error) {
	var obj object
	status, err := s.call(ctx, http.MethodGet, s.collection()+"/"+SecretName(path), nil, &obj)
	if err != nil {
		return obj, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return obj, secretstore.ErrNotFound
	default:
		return obj, fmt.Errorf("%w: read answered %d", secretstore.ErrUnavailable, status)
	}
	if !owned(obj, path) {
		return obj, fmt.Errorf("%w: Secret %s was not created by Ploeg for this path", secretstore.ErrOutsidePath, obj.Metadata.Name)
	}
	return obj, nil
}

func owned(obj object, path string) bool {
	return obj.Metadata.Labels[ManagedByLabel] == ManagedBy && obj.Metadata.Labels[StoreLabel] == StoreValue &&
		obj.Metadata.Annotations[PathAnnotation] == path && obj.Metadata.Name == SecretName(path)
}

func versionOf(obj object) int {
	v, _ := strconv.Atoi(obj.Metadata.Annotations[VersionAnnotation])
	return v
}

func (s *Store) object(path string, version int, data map[string][]byte, resourceVersion string) object {
	return object{APIVersion: "v1", Kind: "Secret", Type: "Opaque", Data: data, Metadata: metadata{
		Name: SecretName(path), Namespace: s.namespace, ResourceVersion: resourceVersion,
		Labels:      map[string]string{ManagedByLabel: ManagedBy, StoreLabel: StoreValue},
		Annotations: map[string]string{PathAnnotation: path, VersionAnnotation: strconv.Itoa(version)},
	}}
}

func (s *Store) collection() string {
	return s.api + "/api/v1/namespaces/" + s.namespace + "/secrets"
}

func (s *Store) call(ctx context.Context, method, target string, in, out any) (int, error) {
	token, err := os.ReadFile(s.tokenFile)
	if err != nil {
		return 0, fmt.Errorf("%w: read service account token", secretstore.ErrUnavailable)
	}
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %s secret: transport error", secretstore.ErrUnavailable, method)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("%w: read response", secretstore.ErrUnavailable)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
		return 0, fmt.Errorf("%w: %s secret answered %d", secretstore.ErrUnavailable, method, resp.StatusCode)
	}
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, fmt.Errorf("%w: malformed response", secretstore.ErrUnavailable)
		}
	}
	return resp.StatusCode, nil
}

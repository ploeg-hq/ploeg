// Package kubeauth reaches the Kubernetes API from inside a pod with the
// pod's own ServiceAccount: TokenReview for ploegd (ADR-0197) and reading
// pods for the credential gateway.
package kubeauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// Client is an in-cluster Kubernetes API client.
type Client struct {
	API       string
	TokenFile string
	HTTP      *http.Client
}

// InCluster builds a client from the pod's ServiceAccount mount.
func InCluster() (*Client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("kubeauth: not running in a cluster (KUBERNETES_SERVICE_HOST unset)")
	}
	pem, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("kubeauth: read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("kubeauth: cluster CA holds no certificate")
	}
	return &Client{
		API:       "https://" + net.JoinHostPort(host, port),
		TokenFile: serviceAccountDir + "/token",
		HTTP:      &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("kubeauth: read the ServiceAccount token: %w", err)
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.API, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("kubeauth: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("kubeauth: %s %s answered %d", method, path, resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

// ReviewedToken is who a TokenReview says a token belongs to.
type ReviewedToken struct {
	Username string
}

// ServiceAccount splits a ServiceAccount username into its namespace and
// name.
func (t ReviewedToken) ServiceAccount() (namespace, name string, ok bool) {
	rest, found := strings.CutPrefix(t.Username, "system:serviceaccount:")
	if !found {
		return "", "", false
	}
	namespace, name, ok = strings.Cut(rest, ":")
	return namespace, name, ok && namespace != "" && name != ""
}

// ErrTokenRejected reports a token the API server did not authenticate for
// the audience.
var ErrTokenRejected = errors.New("kubeauth: token not authenticated for the audience")

// Review asks the API server who token belongs to, for audience.
func (c *Client) Review(ctx context.Context, token, audience string) (ReviewedToken, error) {
	body := map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenReview",
		"spec":       map[string]any{"token": token, "audiences": []string{audience}},
	}
	var out struct {
		Status struct {
			Authenticated bool     `json:"authenticated"`
			Audiences     []string `json:"audiences"`
			User          struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"status"`
	}
	if err := c.do(ctx, http.MethodPost, "/apis/authentication.k8s.io/v1/tokenreviews", body, &out); err != nil {
		return ReviewedToken{}, err
	}
	if !out.Status.Authenticated || !contains(out.Status.Audiences, audience) {
		return ReviewedToken{}, ErrTokenRejected
	}
	return ReviewedToken{Username: out.Status.User.Username}, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Pod is what the credential gateway reads of a pod.
type Pod struct {
	Name        string
	UID         string
	IP          string
	Phase       string
	Labels      map[string]string
	Annotations map[string]string
	OwnerKind   string
	Deleting    bool
}

// PodsAt lists the pods of namespace whose IP is ip and that match selector.
func (c *Client) PodsAt(ctx context.Context, namespace, selector, ip string) ([]Pod, error) {
	q := url.Values{"fieldSelector": {"status.podIP=" + ip}}
	if selector != "" {
		q.Set("labelSelector", selector)
	}
	var out struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				UID               string            `json:"uid"`
				Labels            map[string]string `json:"labels"`
				Annotations       map[string]string `json:"annotations"`
				DeletionTimestamp *string           `json:"deletionTimestamp"`
				OwnerReferences   []struct {
					Kind       string `json:"kind"`
					Controller bool   `json:"controller"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
				PodIP string `json:"podIP"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(namespace)+"/pods?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	pods := make([]Pod, 0, len(out.Items))
	for _, item := range out.Items {
		p := Pod{Name: item.Metadata.Name, UID: item.Metadata.UID, IP: item.Status.PodIP, Phase: item.Status.Phase,
			Labels: item.Metadata.Labels, Annotations: item.Metadata.Annotations, Deleting: item.Metadata.DeletionTimestamp != nil}
		for _, o := range item.Metadata.OwnerReferences {
			if o.Controller {
				p.OwnerKind = o.Kind
			}
		}
		pods = append(pods, p)
	}
	return pods, nil
}

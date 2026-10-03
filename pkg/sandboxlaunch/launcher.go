// Package sandboxlaunch starts one Run on kubernetes-sigs/agent-sandbox: it
// creates a single cold SandboxClaim for the launcher's Job and waits for it to
// finish (docs/contracts/executor.md, the agent-sandbox executor).
package sandboxlaunch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	claimAPIVersion = "extensions.agents.x-k8s.io/v1beta1"
	claimsPath      = "/apis/extensions.agents.x-k8s.io/v1beta1/namespaces/%s/sandboxclaims"
	finished        = "Finished"
)

// ServiceAccountDir is where Kubernetes mounts a pod's service-account token
// and cluster CA.
const ServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// Config describes the one claim a launcher creates.
type Config struct {
	APIBase                 string
	Token                   string
	HTTPClient              *http.Client
	Namespace               string
	ClaimName               string
	JobName                 string
	JobUID                  string
	WarmPool                string
	RunDeadline             time.Duration
	StartTimeout            time.Duration
	ShutdownMargin          time.Duration
	TTLSecondsAfterFinished int32
	PollInterval            time.Duration
	Now                     func() time.Time
}

// InCluster returns an API base, bearer token and HTTP client built from the
// pod's service-account mount under dir.
func InCluster(dir string) (string, string, *http.Client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return "", "", nil, errors.New("not running in a cluster: KUBERNETES_SERVICE_HOST or KUBERNETES_SERVICE_PORT is unset")
	}
	token, err := os.ReadFile(dir + "/token")
	if err != nil {
		return "", "", nil, fmt.Errorf("read service-account token: %w", err)
	}
	ca, err := os.ReadFile(dir + "/ca.crt")
	if err != nil {
		return "", "", nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return "", "", nil, errors.New("cluster CA holds no certificate")
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	return "https://" + net.JoinHostPort(host, port), strings.TrimSpace(string(token)), client, nil
}

// ClaimNameFor returns the claim name for the launcher pod named podName. The
// agent-sandbox controller names the worker pod after the claim, so the claim
// must never share a name with the launcher pod. The name keeps podName's
// unique tail and fits a 63-character pod hostname.
func ClaimNameFor(podName string) string {
	const prefix, limit = "sbx-", 63
	if podName == "" {
		return ""
	}
	if len(prefix)+len(podName) > limit {
		podName = strings.TrimLeft(podName[len(podName)-(limit-len(prefix)):], "-.")
	}
	return prefix + podName
}

// ErrNeverReady reports a claim that did not become Ready within the start
// timeout. The launcher deletes such a claim.
var ErrNeverReady = errors.New("sandbox claim never became ready")

// Launch creates the claim and waits until it finishes, disappears or the
// context ends. It returns an error when the claim could not be created or
// did not become Ready within StartTimeout; it never creates a second claim.
func Launch(ctx context.Context, cfg Config, log *slog.Logger) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := cfg.create(ctx); err != nil {
		return fmt.Errorf("create sandbox claim %s: %w", cfg.ClaimName, err)
	}
	log.Info("sandbox claim created", "claim", cfg.ClaimName, "warm_pool", cfg.WarmPool)
	return cfg.wait(ctx, log)
}

func (cfg Config) validate() error {
	missing := []string{}
	for name, value := range map[string]string{
		"API base": cfg.APIBase, "namespace": cfg.Namespace, "claim name": cfg.ClaimName,
		"job name": cfg.JobName, "job UID": cfg.JobUID, "warm pool": cfg.WarmPool,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("sandbox launcher is missing %s", strings.Join(missing, ", "))
	}
	if cfg.RunDeadline <= 0 {
		return errors.New("sandbox launcher needs a positive run deadline")
	}
	return nil
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

func (cfg Config) claim() map[string]any {
	lifecycle := map[string]any{
		"shutdownPolicy": "Delete",
		"shutdownTime":   cfg.now().Add(cfg.RunDeadline + cfg.ShutdownMargin).UTC().Format(time.RFC3339),
	}
	if cfg.TTLSecondsAfterFinished > 0 {
		lifecycle["ttlSecondsAfterFinished"] = cfg.TTLSecondsAfterFinished
	}
	return map[string]any{
		"apiVersion": claimAPIVersion,
		"kind":       "SandboxClaim",
		"metadata": map[string]any{
			"name":      cfg.ClaimName,
			"namespace": cfg.Namespace,
			"ownerReferences": []map[string]any{{
				"apiVersion":         "batch/v1",
				"kind":               "Job",
				"name":               cfg.JobName,
				"uid":                cfg.JobUID,
				"blockOwnerDeletion": false,
			}},
		},
		"spec": map[string]any{
			"warmPoolRef": map[string]any{"name": cfg.WarmPool},
			"lifecycle":   lifecycle,
		},
	}
}

func (cfg Config) create(ctx context.Context) error {
	body, err := json.Marshal(cfg.claim())
	if err != nil {
		return err
	}
	status, respBody, err := cfg.do(ctx, http.MethodPost, cfg.collection(), body)
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("kubernetes answered %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (cfg Config) wait(ctx context.Context, log *slog.Logger) error {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	startBy := cfg.now().Add(cfg.StartTimeout)
	started := false
	for {
		select {
		case <-ctx.Done():
			log.Info("sandbox launcher stopping before its claim finished; the claim's backstops remove it", "claim", cfg.ClaimName, "reason", context.Cause(ctx))
			return nil
		case <-ticker.C:
		}
		state, err := cfg.state(ctx)
		if err != nil {
			log.Warn("sandbox claim status unavailable", "claim", cfg.ClaimName, "err", err)
			continue
		}
		switch state.phase {
		case claimGone:
			log.Info("sandbox claim is gone", "claim", cfg.ClaimName)
			return nil
		case claimFinished:
			cfg.release(ctx, log, "finished")
			return nil
		}
		if state.ready && !started {
			started = true
			log.Info("sandbox claim ready", "claim", cfg.ClaimName)
		}
		if !started && cfg.StartTimeout > 0 && !cfg.now().Before(startBy) {
			log.Error("sandbox claim never became ready; deleting it", "claim", cfg.ClaimName, "start_timeout", cfg.StartTimeout, "reason", state.reason, "message", state.message)
			cfg.release(ctx, log, "unready")
			return fmt.Errorf("%w within %s: %s: %s", ErrNeverReady, cfg.StartTimeout, state.reason, state.message)
		}
	}
}

type claimPhase int

const (
	claimRunning claimPhase = iota
	claimFinished
	claimGone
)

type claimState struct {
	phase   claimPhase
	ready   bool
	reason  string
	message string
}

func (cfg Config) state(ctx context.Context) (claimState, error) {
	status, body, err := cfg.do(ctx, http.MethodGet, cfg.item(), nil)
	if err != nil {
		return claimState{}, err
	}
	if status == http.StatusNotFound {
		return claimState{phase: claimGone}, nil
	}
	if status != http.StatusOK {
		return claimState{}, fmt.Errorf("kubernetes answered %d", status)
	}
	var claim struct {
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &claim); err != nil {
		return claimState{}, fmt.Errorf("decode sandbox claim: %w", err)
	}
	state := claimState{}
	for _, c := range claim.Status.Conditions {
		switch {
		case c.Type == finished && c.Status == "True":
			state.phase = claimFinished
		case c.Type == "Ready":
			state.ready = c.Status == "True"
			state.reason, state.message = c.Reason, c.Message
		}
	}
	return state, nil
}

func (cfg Config) release(ctx context.Context, log *slog.Logger, why string) {
	status, body, err := cfg.do(ctx, http.MethodDelete, cfg.item(), []byte(`{"propagationPolicy":"Background"}`))
	switch {
	case err != nil:
		log.Warn(why+" sandbox claim not deleted; its backstops remove it", "claim", cfg.ClaimName, "err", err)
	case status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNotFound:
		log.Warn(why+" sandbox claim not deleted; its backstops remove it", "claim", cfg.ClaimName, "status", status, "body", strings.TrimSpace(string(body)))
	default:
		log.Info(why+" sandbox claim deleted", "claim", cfg.ClaimName)
	}
}

func (cfg Config) collection() string {
	return strings.TrimRight(cfg.APIBase, "/") + fmt.Sprintf(claimsPath, url.PathEscape(cfg.Namespace))
}

func (cfg Config) item() string {
	return cfg.collection() + "/" + url.PathEscape(cfg.ClaimName)
}

func (cfg Config) do(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, respBody, err
}

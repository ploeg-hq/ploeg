package worker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// KeyIsolationProxy is the PLOEG_LLM_KEY_ISOLATION value that keeps the
// per-Run model key inside the worker: the harness receives a placeholder and
// a loopback base URL, and the worker's proxy swaps in the real key.
const KeyIsolationProxy = "proxy"

type llmKeyProxy struct {
	server      *http.Server
	baseURL     string
	placeholder string
}

func startLLMKeyProxy(upstream, key string, activity *harness.Activity) (*llmKeyProxy, error) {
	if key == "" {
		return nil, errors.New("no model key to isolate")
	}
	placeholder, err := randomPlaceholder()
	if err != nil {
		return nil, err
	}
	return startLLMProxy(upstream, key, placeholder, activity)
}

func startLLMObserver(upstream string, activity *harness.Activity) (*llmKeyProxy, error) {
	return startLLMProxy(upstream, "", "", activity)
}

func startLLMProxy(upstream, key, placeholder string, activity *harness.Activity) (*llmKeyProxy, error) {
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("model gateway URL %q is not absolute", upstream)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for the model key proxy: %w", err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.Host = target.Host
			if key != "" {
				swapModelKey(r.Out.Header, key)
			}
			activity.Touch()
		},
		ModifyResponse: func(resp *http.Response) error {
			activity.Touch()
			resp.Body = activityBody{ReadCloser: resp.Body, activity: activity}
			return nil
		},
		FlushInterval: -1,
	}
	var handler http.Handler = proxy
	if placeholder != "" {
		handler = requireModelPlaceholder(placeholder, proxy)
	}
	p := &llmKeyProxy{
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 30 * time.Second,
		},
		baseURL:     "http://" + ln.Addr().String() + strings.TrimRight(target.Path, "/"),
		placeholder: placeholder,
	}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

func (p *llmKeyProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
}

type activityBody struct {
	io.ReadCloser
	activity *harness.Activity
}

func (b activityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.activity.Touch()
	}
	return n, err
}

func requireModelPlaceholder(placeholder string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !presentsModelPlaceholder(r.Header, placeholder) {
			http.Error(w, "this Run's model proxy serves only requests that present the Run's placeholder", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func presentsModelPlaceholder(h http.Header, placeholder string) bool {
	bearer, isBearer := authorizationCredential(h, "Bearer")
	return (isBearer && equalSecret(bearer, placeholder)) || equalSecret(h.Get("X-Api-Key"), placeholder)
}

func authorizationCredential(h http.Header, scheme string) (string, bool) {
	value := h.Get("Authorization")
	if len(value) <= len(scheme) || value[len(scheme)] != ' ' || !strings.EqualFold(value[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(value[len(scheme)+1:]), true
}

func equalSecret(presented, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

func swapModelKey(h http.Header, key string) {
	anthropic := h.Get("X-Api-Key") != ""
	h.Del("Authorization")
	h.Del("X-Api-Key")
	h.Del("Api-Key")
	if anthropic {
		h.Set("X-Api-Key", key)
		return
	}
	h.Set("Authorization", "Bearer "+key)
}

func randomPlaceholder() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate model key placeholder: %w", err)
	}
	return "ploeg-isolated-" + hex.EncodeToString(b), nil
}

func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}

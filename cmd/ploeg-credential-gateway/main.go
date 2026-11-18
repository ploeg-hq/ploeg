package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/credentialgateway"
	"github.com/ploeg-hq/ploeg/pkg/kubeauth"
	"github.com/ploeg-hq/ploeg/pkg/mcpstdio"
)

var version = "0.0.0-dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("ploeg-credential-gateway exiting", "err", err)
		os.Exit(1)
	}
}

type listener struct {
	addr    string
	handler http.Handler
}

func run(log *slog.Logger) error {
	if err := mcpstdio.Undumpable(); err != nil {
		return fmt.Errorf("keep the gateway's memory and environment from other processes: %w", err)
	}
	cfg, err := configFromEnv(log)
	if err != nil {
		return err
	}
	pods, err := kubeauth.InCluster()
	if err != nil {
		return err
	}
	cfg.Pods = pods
	g, err := credentialgateway.New(cfg)
	if err != nil {
		return err
	}
	listeners := []listener{
		{envOr("PLOEG_GATEWAY_CONTROL_ADDR", ":8080"), g.ControlHandler()},
		{envOr("PLOEG_GATEWAY_FORGE_ADDR", ":8081"), g.Handler(credentialgateway.FamilyForge)},
		{envOr("PLOEG_GATEWAY_HEALTH_ADDR", ":8090"), health()},
	}
	listeners = append(listeners, listener{envOr("PLOEG_GATEWAY_MCP_ADDR", ":8083"), g.Handler(credentialgateway.FamilyMCP)})
	if session := g.SessionHandler(); session != nil {
		listeners = append(listeners, listener{envOr("PLOEG_GATEWAY_SESSION_ADDR", ":8084"), session})
	}
	if model := g.Handler(credentialgateway.FamilyModel); model != nil {
		listeners = append(listeners, listener{envOr("PLOEG_GATEWAY_MODEL_ADDR", ":8082"), model})
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("ploeg-credential-gateway starting", "version", version, "namespace", cfg.Namespace, "bootstraps", len(cfg.Bootstraps))
	return serve(ctx, log, g, listeners)
}

func serve(ctx context.Context, log *slog.Logger, g *credentialgateway.Gateway, listeners []listener) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(listeners))
	servers := make([]*http.Server, 0, len(listeners))
	for _, l := range listeners {
		srv := &http.Server{Addr: l.addr, Handler: l.handler, ReadHeaderTimeout: 30 * time.Second}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("listen on %s: %w", srv.Addr, err)
			}
		}()
	}
	auditDone := make(chan struct{})
	go func() {
		g.RunAudit(ctx)
		close(auditDone)
	}()
	var failed error
	select {
	case <-ctx.Done():
	case failed = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
	wg.Wait()
	if failed == nil {
		<-auditDone
	}
	log.Info("ploeg-credential-gateway stopped")
	return failed
}

func health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func configFromEnv(log *slog.Logger) (credentialgateway.Config, error) {
	limits, err := credentialgateway.ParseLimits(os.Getenv("PLOEG_GATEWAY_LIMITS"))
	if err != nil {
		return credentialgateway.Config{}, err
	}
	bootstraps, err := readBootstraps(envOr("PLOEG_GATEWAY_BOOTSTRAP_DIR", "/var/run/secrets/ploeg/bootstraps"))
	if err != nil {
		return credentialgateway.Config{}, err
	}
	cfg := credentialgateway.Config{
		Namespace:          os.Getenv("POD_NAMESPACE"),
		PloegdURL:          os.Getenv("PLOEG_API_URL"),
		TokenFile:          envOr("PLOEG_GATEWAY_TOKEN_FILE", "/var/run/secrets/ploeg/gateway/token"),
		Bootstraps:         bootstraps,
		ModelURL:           os.Getenv("PLOEG_GATEWAY_MODEL_URL"),
		RunPodSelector:     envOr("PLOEG_GATEWAY_RUN_POD_SELECTOR", "app.kubernetes.io/name=ploeg-worker"),
		SessionPodSelector: os.Getenv("PLOEG_GATEWAY_SESSION_POD_SELECTOR"),
		Limits:             limits,
		ScratchDir:         envOr("TMPDIR", os.TempDir()),
		Log:                log,
	}
	if cfg.Namespace == "" || cfg.PloegdURL == "" {
		return cfg, errors.New("POD_NAMESPACE and PLOEG_API_URL are required")
	}
	return cfg, nil
}

func readBootstraps(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the bootstrap capabilities in %s: %w", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.Contains(name, "--") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if token := strings.TrimSpace(string(raw)); token != "" {
			out[name] = token
		}
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

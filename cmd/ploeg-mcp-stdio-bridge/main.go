// Command ploeg-mcp-stdio-bridge is the entrypoint of a sandboxed MCP adapter
// (ADR-0199 section 7): it runs the stdio MCP server named after "--" once
// per MCP session and serves it over Streamable HTTP at /mcp.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/mcpstdio"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("ploeg-mcp-stdio-bridge exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("ploeg-mcp-stdio-bridge", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "address to serve /mcp on")
	sessions := fs.Int("sessions", mcpstdio.DefaultLimit, "concurrent sessions, each one child process")
	idle := fs.Duration("idle", mcpstdio.DefaultIdle, "end a session idle this long")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if len(command) == 0 {
		return errors.New("name the stdio MCP server after --")
	}
	b := &mcpstdio.Bridge{Command: command, Env: os.Environ(), Idle: *idle, Sessions: *sessions, Log: log}
	mux := http.NewServeMux()
	mux.Handle("/mcp", b)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go b.Reap(ctx)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	log.Info("ploeg-mcp-stdio-bridge serving", "listen", *listen, "command", command[0])
	select {
	case <-ctx.Done():
	case err := <-errs:
		return fmt.Errorf("listen on %s: %w", *listen, err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// Command ploeg-mcp-stdio-bridge is the entrypoint of a sandboxed MCP adapter
// (ADR-0199 section 7): it runs the stdio MCP server named after "--" once
// per MCP session and serves it over Streamable HTTP at /mcp. A session's
// process gets PATH, locale, time zone, CA bundle and the variables
// --pass-env names, never the rest of the bridge's environment.
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
	"strings"
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
	b, listen, err := newBridge(args, os.Environ())
	if err != nil {
		return err
	}
	if err := mcpstdio.Undumpable(); err != nil {
		return fmt.Errorf("hide the bridge's environment from its sessions: %w", err)
	}
	b.Log = log
	command := b.Command
	mux := http.NewServeMux()
	mux.Handle("/mcp", b)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go b.Reap(ctx)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	log.Info("ploeg-mcp-stdio-bridge serving", "listen", listen, "command", command[0])
	select {
	case <-ctx.Done():
	case err := <-errs:
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func newBridge(args, environ []string) (*mcpstdio.Bridge, string, error) {
	fs := flag.NewFlagSet("ploeg-mcp-stdio-bridge", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "address to serve /mcp on")
	sessions := fs.Int("sessions", mcpstdio.DefaultLimit, "concurrent sessions, each one child process")
	idle := fs.Duration("idle", mcpstdio.DefaultIdle, "end a session idle this long")
	root := fs.String("sessions-dir", "", "directory that holds each session's own directory; empty is the system temporary directory")
	var pass []string
	fs.Func("pass-env", "a variable of the bridge's environment a session's process gets, such as the server's credential; repeatable", func(name string) error {
		if name == "" || strings.ContainsAny(name, "= ") {
			return fmt.Errorf("%q is not a variable name", name)
		}
		pass = append(pass, name)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	command := fs.Args()
	if len(command) == 0 {
		return nil, "", errors.New("name the stdio MCP server after --")
	}
	return &mcpstdio.Bridge{Command: command, Env: mcpstdio.ChildEnv(environ, pass), Root: *root, Idle: *idle, Sessions: *sessions}, *listen, nil
}

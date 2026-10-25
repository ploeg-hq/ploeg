// Command ploeg-mcp serves Ploeg's operator API as MCP tools.
//
// Without arguments it speaks MCP over stdio for a person's own AI client,
// reading PLOEG_URL and PLOEG_MCP_TOKEN, the bearer credential of an Operator
// Consumer that should not execute.
//
// With the argument http it serves stateless Streamable HTTP as an OAuth
// resource server: it verifies the caller's access token from
// PLOEG_MCP_ISSUER, maps the caller's groups to Operator Consumers through
// PLOEG_MCP_GROUPS, and calls ploegd with that consumer's token, never the
// caller's. A group marked execute also gets action tools, which name the
// person and their client to ploegd and act only after the person confirms
// the exact effect through an MCP elicitation (ADR-0108), or, with
// PLOEG_MCP_CONFIRMATION_MODE=login, after the person signs in afresh at
// ploeg-mcp's confirmation page (ADR-0153). Stdio mode only reads.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "http" {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		cfg, err := loadHTTPConfig(os.Getenv, os.LookupEnv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ploeg-mcp http:", err)
			os.Exit(2)
		}
		if err := serveHTTP(ctx, cfg, logger); err != nil {
			fmt.Fprintln(os.Stderr, "ploeg-mcp http:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "ploeg-mcp: unknown argument; run it without arguments for stdio, or with http for Streamable HTTP")
		os.Exit(2)
	}
	client, err := operatorclient.New(os.Getenv("PLOEG_URL"), os.Getenv("PLOEG_MCP_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ploeg-mcp: set PLOEG_URL and PLOEG_MCP_TOKEN:", err)
		os.Exit(2)
	}
	if err := newServer(client).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "ploeg-mcp:", err)
		os.Exit(1)
	}
}

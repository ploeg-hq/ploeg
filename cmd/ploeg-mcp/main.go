// Command ploeg-mcp serves Ploeg's operator API as read-only MCP tools over
// stdio, for a person's own AI client. It reads PLOEG_URL and PLOEG_MCP_TOKEN,
// the bearer credential of an Operator Consumer that should not execute.
package main

import (
	"context"
	"fmt"
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
	client, err := operatorclient.New(os.Getenv("PLOEG_URL"), os.Getenv("PLOEG_MCP_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ploeg-mcp: set PLOEG_URL and PLOEG_MCP_TOKEN:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newServer(client).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "ploeg-mcp:", err)
		os.Exit(1)
	}
}

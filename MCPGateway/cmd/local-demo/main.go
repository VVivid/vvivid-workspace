// local-demo simulates an MCP agent with development-only loopback identities.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/VVivid/vvivid-workspace/MCPGateway/internal/localdemo"
)

func main() {
	binary := flag.String("gateway", "./bin/mcp-gateway", "path to the production gateway executable")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: local-demo [--gateway ./bin/mcp-gateway]")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := localdemo.Run(ctx, *binary, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Local demo failed:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: local agent scenario completed; temporary keys, listeners and gateway process cleaned up")
}

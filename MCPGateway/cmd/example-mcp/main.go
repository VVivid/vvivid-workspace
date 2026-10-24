// example-mcp is a small gateway-authenticated reference backend.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/internal/examplemcp"
	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8099", "loopback listener")
	issuer := flag.String("gateway-issuer", "", "trusted gateway origin")
	audience := flag.String("audience", "example-mcp", "backend audience")
	keys := flag.String("jwks", "", "public gateway JWKS file")
	client := flag.String("client-id", "example-agent", "allowed OAuth client")
	scope := flag.String("required-scope", "example:use", "required granted scope")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() || flag.NArg() != 0 {
		return errors.New("example backend requires an IP loopback listener and no positional arguments")
	}
	handler, err := examplemcp.New(trust.Config{Issuer: *issuer, Audience: *audience, JWKSFile: *keys}, *client, *scope, true)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

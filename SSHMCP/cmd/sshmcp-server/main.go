// sshmcp-server is the SSH execution MCP backend for a shared gateway.
// It does not perform OIDC discovery or run a Keycloak compatibility proxy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sshmcp/internal/gateway"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("config", "", "private backend JSON config")
	flag.Parse()
	if *file == "" || flag.NArg() != 0 {
		return errors.New("usage: sshmcp-server --config /private/backend.json")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	var c gateway.Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return err
	}
	if c.Gateway == nil {
		return errors.New("backend mode requires gateway trust")
	}
	verifier, err := gateway.NewBackendTokenVerifier(*c.Gateway)
	if err != nil {
		return err
	}
	rpc, err := gateway.NewSSHRunner(c.SSH)
	if err != nil {
		return err
	}
	defer rpc.Close()
	handler, err := gateway.NewHandler(c, rpc, verifier)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			server.Close()
		}
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

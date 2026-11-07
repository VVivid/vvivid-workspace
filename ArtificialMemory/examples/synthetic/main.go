// Command synthetic runs an invented agent scenario against a temporary,
// loopback-only instance of the same authenticated service used in production.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/examples/agent"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/service"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/store/sqlite"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Synthetic example failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	private, err := os.MkdirTemp("", "artificial-memory-synthetic-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	fixture, err := agent.NewIdentity()
	if err != nil {
		return err
	}
	public, err := fixture.PublicJWKS()
	if err != nil {
		return err
	}
	jwks := filepath.Join(private, "public.jwks")
	if err := os.WriteFile(jwks, public, 0600); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, filepath.Join(private, "sqlite", "memory.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.EnrollAccount(ctx, fixture.AccountIdentity()); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg := service.Config{
		Listen: listener.Addr().String(), Database: filepath.Join(private, "sqlite", "memory.db"), BackendOrigin: agent.BackendOrigin,
		Identity: identity.Config{GatewayIssuer: agent.GatewayIssuer, Audience: agent.Audience, IdentityIssuer: agent.IdentityIssuer, JWKSFile: jwks, AllowedClients: []string{agent.ClientID}},
	}
	handler, err := service.NewHandler(cfg, store)
	if err != nil {
		return err
	}
	server := service.HTTPServer(cfg, handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
		<-done
	}()
	session, err := fixture.Connect(ctx, "http://"+listener.Addr().String(), "memory:read memory:write")
	if err != nil {
		return err
	}
	defer session.Close()
	_, err = agent.RunScenario(ctx, session, os.Stdout)
	if err != nil {
		return err
	}
	return nil
}

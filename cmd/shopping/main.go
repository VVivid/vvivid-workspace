package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentshoppinglist/internal/buildinfo"
	"agentshoppinglist/internal/config"
	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		if err := command(os.Args[1:]); err != nil {
			logger.Error("shopping command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("shopping stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) (err error) {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	store, err := sqlite.Open(ctx, cfg.DatabasePath, cfg.DatabaseBusyTimeout)
	if err != nil {
		return fmt.Errorf("persistence: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	logger.Info("shopping starting", "build", buildinfo.Current(), "migration_backup", store.MigrationBackup)
	var auth *identity.Manager
	if cfg.Identity != nil {
		auth, err = identity.New(ctx, *cfg.Identity, store)
		if err != nil {
			return fmt.Errorf("identity: %w", err)
		}
	}
	handler, err := web.NewHandler(logger, auth, shopping.New(store))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("shopping listening", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		logger.Info("shopping shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", errors.Join(err, server.Close()))
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func command(args []string) error {
	if len(args) == 1 && args[0] == "version" {
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	}
	if len(args) == 2 && args[0] == "healthcheck" && (args[1] == "live" || args[1] == "ready") {
		// The health command is intentionally loopback-only; it needs no identity
		// secret, database handle or external identity-provider availability.
		address := os.Getenv("SHOPPING_HTTP_ADDR")
		if address == "" {
			address = "127.0.0.1:8090"
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("invalid healthcheck listener address")
		}
		loopback := "127.0.0.1"
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			loopback = host
		}
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://" + net.JoinHostPort(loopback, port) + "/health/" + args[1])
		if err != nil {
			return fmt.Errorf("healthcheck unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("healthcheck returned %d", response.StatusCode)
		}
		return nil
	}
	return fmt.Errorf("usage: shopping [version|healthcheck live|healthcheck ready]")
}

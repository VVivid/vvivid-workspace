package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/gateway"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	file := flag.String("config", "", "gateway JSON configuration")
	flag.Parse()
	if *file == "" || flag.NArg() != 0 {
		return errors.New("usage: mcp-gateway --config /config/gateway.json")
	}
	cfg, err := gateway.Load(*file)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	discovery, cancel := context.WithTimeout(ctx, 15*time.Second)
	authenticator, err := gateway.NewOIDCAuthenticator(discovery, cfg.Issuer)
	cancel()
	if err != nil {
		return err
	}
	handler, err := gateway.New(cfg, authenticator, logger)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	logger.Info("gateway listening", "address", cfg.Listen)
	err = server.ListenAndServe()
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

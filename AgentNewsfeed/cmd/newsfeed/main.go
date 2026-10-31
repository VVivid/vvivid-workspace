package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		if err := command(os.Args[1:]); err != nil {
			logger.Error("newsfeed command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("newsfeed stopped", "error", err)
		os.Exit(1)
	}
}

func command(args []string) error {
	if len(args) == 1 && args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if len(args) == 2 && args[0] == "healthcheck" && (args[1] == "live" || args[1] == "ready") {
		return healthcheck(context.Background(), setting("NEWSFEED_HTTP_ADDR", "127.0.0.1:8091"), args[1])
	}
	return fmt.Errorf("usage: newsfeed [version|healthcheck live|healthcheck ready]")
}

func setting(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func run(ctx context.Context, logger *slog.Logger) (err error) {
	address := setting("NEWSFEED_HTTP_ADDR", "127.0.0.1:8091")
	host, port, err := net.SplitHostPort(address)
	if err != nil || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
		return fmt.Errorf("NEWSFEED_HTTP_ADDR requires an IP or localhost and port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("NEWSFEED_HTTP_ADDR port must be 1–65535")
	}
	policy, err := thumbnail.NewPolicy(setting("NEWSFEED_THUMBNAIL_HOSTS", ""))
	if err != nil {
		return err
	}
	cfg, err := identity.LoadConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, setting("NEWSFEED_DB_PATH", "data/newsfeed.sqlite"), 2*time.Second)
	if err != nil {
		return fmt.Errorf("persistence: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	var auth *identity.Manager
	if cfg != nil {
		auth, err = identity.New(ctx, *cfg, store)
		if err != nil {
			return err
		}
	}
	handler, err := web.New(logger, auth, feed.New(store, policy), thumbnail.NewProxy(policy))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("newsfeed listening", "address", listener.Addr().String(), "version", version, "identity_configured", auth != nil)
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return errors.Join(err, server.Close())
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

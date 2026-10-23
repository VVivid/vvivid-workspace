package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	trace "github.com/xtrace-example/traceplugin"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "collector" && os.Args[1] != "replica") {
		log.Fatal("usage: demo collector|replica")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var handler http.Handler
	if os.Args[1] == "collector" {
		collector, err := NewCollector(env("DB_PATH", "/data/reports.db"))
		if err != nil {
			log.Fatal(err)
		}
		defer collector.db.Close()
		handler = collector.Handler()
	} else {
		reporter, err := trace.NewReporter(ctx, env("COLLECTOR_URL", "http://collector:8080/reports"))
		if err != nil {
			log.Fatal(err)
		}
		imageAge, err := time.ParseDuration(env("IMAGE_AGE", "0s"))
		if err != nil {
			log.Fatal("IMAGE_AGE: ", err)
		}
		replica, err := NewReplica(env("REPLICA", "A"), imageAge, reporter.Emit)
		if err != nil {
			log.Fatal(err)
		}
		handler = replica.Handler()
	}
	server := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("%s listening on %s", os.Args[1], server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

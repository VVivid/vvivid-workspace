// VocabBank serves static Next.js pages, authenticated APIs and private MCP.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/httpapp"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/identity"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/mcptools"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("VocabBank stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	dbPath := flag.String("db", "/data/vocabbank.sqlite", "SQLite database path")
	staticDir := flag.String("static", "web/out", "Next static export directory")
	listen := flag.String("listen", ":8090", "HTTP listen address")
	enroll := flag.String("enroll-file", "", "Read protected JSON {issuer,subject,label} and enroll owner; exit")
	keygen := flag.String("keygen-file", "", "Create protected session keyring file; exit")
	flag.Parse()

	if *keygen != "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{"active": "v1", "keys": map[string]string{"v1": base64.StdEncoding.EncodeToString(key)}})
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*keygen, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(raw, '\n'))
		return err
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if *enroll != "" {
		raw, err := identity.PrivateFile(*enroll)
		if err != nil {
			return err
		}
		var in struct {
			Issuer  string `json:"issuer"`
			Subject string `json:"subject"`
			Label   string `json:"label"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return fmt.Errorf("invalid enrollment file")
		}
		_, err = s.Enroll(context.Background(), in.Issuer, in.Subject, in.Label)
		if err != nil {
			return err
		}
		slog.Info("Owner enrollment saved")
		return nil
	}
	cfg, err := identity.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	auth, err := identity.New(ctx, cfg, s)
	if err != nil {
		return err
	}
	if _, err = os.Stat(*staticDir + "/index.html"); err != nil {
		return fmt.Errorf("build the Next static export before starting")
	}
	server := &http.Server{Addr: *listen, Handler: httpapp.New(s, auth, mcptools.New(s), httpapp.StaticDir(*staticDir)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("VocabBank listening", "address", *listen)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

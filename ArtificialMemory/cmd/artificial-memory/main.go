// Command artificial-memory serves the private gateway-authenticated MCP
// endpoint or explicitly administers local enrollment. It has no auth bypass.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/service"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/store/sqlite"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer) int {
	if len(args) == 0 || len(args) == 1 && args[0] == "serve" {
		return serve(ctx, getenv, errOut)
	}
	if len(args) == 2 && args[0] == "healthcheck" {
		return healthcheck(ctx, args[1], getenv, errOut)
	}
	if len(args) >= 2 && args[0] == "admin" && (args[1] == "backup" || args[1] == "restore") {
		return recoverJournal(ctx, args[1], args[2:], out, errOut)
	}
	if len(args) >= 2 && args[0] == "admin" && (args[1] == "enroll" || args[1] == "disable") {
		return administer(ctx, args[1], args[2:], out, errOut)
	}
	fmt.Fprintln(errOut, "Usage: artificial-memory [serve] | healthcheck live|ready | admin enroll|disable --database PATH --issuer HTTPS_ISSUER --subject-file PATH | admin backup --database PATH --output NEW_PATH | admin restore --backup PATH --database NEW_PATH")
	return 2
}

func recoverJournal(ctx context.Context, operation string, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "", "journal path")
	var source, destination *string
	if operation == "backup" {
		source = database
		destination = flags.String("output", "", "new private snapshot path")
	} else {
		source = flags.String("backup", "", "existing private snapshot path")
		destination = database
	}
	timeout := flags.Duration("timeout", 5*time.Minute, "snapshot deadline")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *source == "" || *destination == "" || *timeout <= 0 {
		fmt.Fprintln(errOut, "Backup/restore requires a source and a new destination; see usage.")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	copySnapshot := sqlite.Backup
	if operation == "restore" {
		copySnapshot = sqlite.Restore
	}
	if err := copySnapshot(ctx, *source, *destination); err != nil {
		fmt.Fprintln(errOut, "Journal snapshot failed; source must exist and destination must be new and private.")
		return 1
	}
	fmt.Fprintln(out, `{"snapshot":"complete"}`)
	return 0
}

func serve(ctx context.Context, getenv func(string) string, errOut io.Writer) int {
	cfg, err := service.FromEnv(getenv)
	if err != nil {
		fmt.Fprintln(errOut, "Invalid service configuration:", err)
		return 1
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	store, err := sqlite.Open(startup, cfg.Database)
	cancel()
	if err != nil {
		fmt.Fprintln(errOut, "Cannot open journal storage.")
		return 1
	}
	defer store.Close()
	handler, err := service.NewHandler(cfg, store)
	if err != nil {
		fmt.Fprintln(errOut, "Cannot configure trusted MCP service:", err)
		return 1
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(errOut, "Cannot open service listener.")
		return 1
	}
	server := service.HTTPServer(cfg, handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintln(errOut, "ArtificialMemory started.")
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(errOut, "HTTP service failed.")
			return 1
		}
	case <-ctx.Done():
		fmt.Fprintln(errOut, "ArtificialMemory stopping.")
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := server.Shutdown(shutdown)
		cancel()
		if err != nil {
			server.Close()
			fmt.Fprintln(errOut, "HTTP shutdown timed out.")
			return 1
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(errOut, "HTTP service failed.")
			return 1
		}
	}
	fmt.Fprintln(errOut, "ArtificialMemory stopped.")
	return 0
}

func administer(ctx context.Context, operation string, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // Flag errors can contain sensitive command arguments.
	database := flags.String("database", "", "private journal path")
	issuer := flags.String("issuer", "", "exact HTTPS provider issuer")
	subjectFile := flags.String("subject-file", "", "private file containing the original subject")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *database == "" || *subjectFile == "" {
		fmt.Fprintln(errOut, "Admin requires --database, --issuer and --subject-file.")
		return 2
	}
	subject, err := readSubject(*subjectFile)
	id := account.Identity{Issuer: *issuer, Subject: subject}
	if err != nil || id.Validate() != nil {
		fmt.Fprintln(errOut, "Invalid identity or private subject file.")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, *database)
	if err != nil {
		fmt.Fprintln(errOut, "Cannot open journal storage.")
		return 1
	}
	defer store.Close()
	if operation == "enroll" {
		accountID, err := store.EnrollAccount(ctx, id)
		if err != nil {
			fmt.Fprintln(errOut, "Cannot enroll account.")
			return 1
		}
		if json.NewEncoder(out).Encode(struct {
			AccountID string `json:"account_id"`
			Active    bool   `json:"active"`
		}{accountID, true}) != nil {
			fmt.Fprintln(errOut, "Cannot write enrollment result; retry the same identity.")
			return 1
		}
		return 0
	}
	found, err := store.DisableAccount(ctx, id)
	if err != nil || !found {
		fmt.Fprintln(errOut, "Cannot disable account; enrollment must already exist.")
		return 1
	}
	fmt.Fprintln(out, `{"active":false}`)
	return 0
}

func readSubject(path string) (string, error) {
	invalid := errors.New("invalid private subject file")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 257 {
		return "", invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, 258))
	if err != nil || len(data) > 257 {
		return "", invalid
	}
	subject := string(data)
	if strings.HasSuffix(subject, "\n") {
		subject = strings.TrimSuffix(strings.TrimSuffix(subject, "\n"), "\r")
	}
	return subject, nil
}

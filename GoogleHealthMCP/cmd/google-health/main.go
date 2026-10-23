package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/oudavid/GoogleHealthMCP/internal/collector"
)

func main() {
	syscall.Umask(0077)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run(ctx context.Context, args []string, output io.Writer) int {
	encoder := json.NewEncoder(output)
	emit := func(v any) { _ = encoder.Encode(v) }
	fail := func(err error) int {
		status, sqliteCode := collector.DiagnosticCodes(err)
		message := map[string]any{"status": "error", "error_code": collector.Code(err)}
		if status != 0 {
			message["http_status"] = status
		}
		if sqliteCode != 0 {
			message["sqlite_code"] = sqliteCode
		}
		emit(message)
		return 1
	}
	if len(args) == 0 || !slices.Contains([]string{"authorize", "backfill", "sync", "status"}, args[0]) {
		emit(map[string]string{"status": "usage", "commands": "authorize | backfill | sync | status"})
		return 2
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("db", env("GOOGLE_HEALTH_DB", "./data/health.sqlite3"), "SQLite path")
	client := flags.String("client", env("GOOGLE_HEALTH_CLIENT_FILE", "./secrets/client.json"), "OAuth client JSON path")
	tokens := flags.String("tokens", env("GOOGLE_HEALTH_TOKEN_DIR", "./secrets/tokens"), "Mutable private token directory")
	timeout := flags.Duration("timeout", 10*time.Hour, "Maximum runtime; progress is resumable")
	var options collector.Options
	var live, noBrowser, verifyOnly, requireAuthorized bool
	var port, publishing string
	switch command {
	case "authorize":
		flags.StringVar(&port, "port", "8765", "Loopback callback port")
		flags.BoolVar(&noBrowser, "no-browser", false, "Open emitted URL manually through an SSH tunnel")
		flags.BoolVar(&verifyOnly, "verify-only", false, "Recheck saved pending tokens and a real health sample")
		flags.StringVar(&publishing, "publishing-status", env("GOOGLE_HEALTH_OAUTH_PUBLISHING_STATUS", "testing"), "Owner-confirmed OAuth publishing status: testing or production")
	case "backfill":
		flags.StringVar(&options.Since, "since", "", "Optional bounded repair start date; leaves the saved history scope unchanged")
		flags.StringVar(&options.HistorySince, "history-since", "", "Save an inclusive start date for default source and aggregate history")
		flags.StringVar(&options.Until, "until", "", "Exclusive end date")
		flags.StringVar(&options.AggregateSince, "aggregate-since", "", "Owner-confirmed earliest date for aggregate-only metrics")
		flags.IntVar(&options.MaxPages, "max-pages", 0, "Page budget, zero for unlimited; budget exhaustion pauses cleanly")
		flags.BoolVar(&options.Force, "force", false, "Refresh already completed windows in this historical plan")
	case "status":
		flags.BoolVar(&live, "check", false, "Check current Google identity and token access")
		flags.BoolVar(&requireAuthorized, "require-authorized", false, "Fail unless identity/sample, all scopes, and refresh credentials are verified")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			emit(map[string]string{"status": "help", "documentation": "README.md", "command": command})
			return 0
		}
		emit(map[string]string{"status": "error", "error_code": "invalid_arguments"})
		return 2
	}
	if flags.NArg() != 0 || options.MaxPages < 0 || *timeout <= 0 || (command == "authorize" && !slices.Contains([]string{"testing", "production"}, publishing)) {
		emit(map[string]string{"status": "error", "error_code": "invalid_arguments"})
		return 2
	}
	for _, date := range []string{options.Since, options.HistorySince, options.Until, options.AggregateSince} {
		if date != "" {
			if _, err := collector.ParseDate(date); err != nil {
				return fail(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var store *collector.Store
	var err error
	_, statErr := os.Stat(*database)
	if command == "status" && !live && statErr == nil {
		store, err = collector.OpenReadOnlyStore(ctx, *database)
	} else {
		lock, lockErr := collector.AcquireLock(*database)
		if lockErr != nil {
			return fail(lockErr)
		}
		defer lock.Close()
		if command != "status" || live {
			tokenLock, lockErr := collector.AcquireLock(*tokens + "/token-store")
			if lockErr != nil {
				return fail(lockErr)
			}
			defer tokenLock.Close()
		}
		store, err = collector.OpenStore(ctx, *database)
	}
	if err != nil {
		return fail(err)
	}
	defer store.Close()
	h := collector.NewHTTP()
	var oauth *collector.OAuth
	if command == "status" && !live {
		state, err := collector.ReadTokens(*tokens)
		if err != nil {
			return fail(err)
		}
		oauth = &collector.OAuth{Tokens: state, TokenDir: *tokens, HTTP: h}
	} else {
		oauth, err = collector.NewOAuth(*client, *tokens, h)
		if err != nil {
			return fail(err)
		}
	}
	engine := collector.NewEngine(store, oauth, collector.NewAPI(oauth))
	switch command {
	case "authorize":
		if !verifyOnly {
			oauth.Tokens.PublishingStatus = publishing
			authContext, authCancel := context.WithTimeout(ctx, 10*time.Minute)
			defer authCancel()
			if err := oauth.Authorize(authContext, port, noBrowser, func(event collector.AuthorizationEvent) { emit(event) }); err != nil {
				return fail(err)
			}
		}
		if err := engine.VerifyAuthorization(ctx); err != nil {
			return fail(err)
		}
		status, err := engine.Status(ctx, false)
		if err != nil {
			return fail(err)
		}
		status.Status, status.AuthorizationCheckedLive = "authorized", true
		emit(status)
	case "status":
		status, err := engine.Status(ctx, live)
		if err != nil {
			return fail(err)
		}
		emit(status)
		if requireAuthorized && (!status.IdentityAndSampleVerified || !status.CredentialsUsable || len(status.MissingScopes) != 0) {
			return 1
		}
	default:
		engine.Progress = func(result collector.Result) { emit(result) }
		var result collector.Result
		if command == "sync" {
			result, err = engine.Sync(ctx, options)
		} else {
			result, err = engine.Backfill(ctx, options)
		}
		if err != nil {
			result.Status, result.ErrorCode = "failed", collector.Code(err)
			result.HTTPStatus, result.SQLiteCode = collector.DiagnosticCodes(err)
		}
		emit(result)
		if err != nil {
			return 1
		}
	}
	return 0
}

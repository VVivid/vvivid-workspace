// shopping-admin is an operator-only local provisioning command. It is never
// invoked by HTTP or MCP and requires direct access to the private database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agentshoppinglist/internal/store/sqlite"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) (err error) {
	if len(args) > 0 {
		switch args[0] {
		case "backup", "verify", "restore", "inspect", "version":
			return recoveryCommand(ctx, args, output)
		}
	}
	if len(args) == 0 || (args[0] != "enroll" && args[0] != "disable") {
		return fmt.Errorf("usage: shopping-admin enroll|disable --db PATH --issuer URL --subject-file PATH [--timezone IANA --currency ISO --retailer NAME --week-start DAY]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("db", "", "private SQLite file")
	issuer := flags.String("issuer", "", "exact verified OIDC issuer")
	subjectPath := flags.String("subject-file", "", "private file containing the exact Keycloak subject")
	timezone := flags.String("timezone", "", "explicit IANA timezone")
	currency := flags.String("currency", "", "explicit ISO 4217 currency")
	retailer := flags.String("retailer", "", "explicit preferred retailer")
	weekStart := flags.String("week-start", "", "explicit first weekday")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *path == "" || *issuer == "" || *subjectPath == "" {
		return fmt.Errorf("explicit --db, --issuer and --subject-file are required")
	}
	file, err := os.Open(*subjectPath)
	if err != nil {
		return fmt.Errorf("cannot open subject file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 256 {
		_ = file.Close()
		return fmt.Errorf("subject file must be a private regular file of at most 256 bytes")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 257))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return fmt.Errorf("cannot read subject file")
	}
	// Permit a text file's final newline, but preserve all other subject bytes.
	subject := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	store, err := sqlite.Open(ctx, *path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("open enrollment database: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	identity := sqlite.ExternalIdentity{Issuer: *issuer, Subject: subject}
	if args[0] == "disable" {
		if err := store.DisableAccount(ctx, identity); err != nil {
			return fmt.Errorf("disable enrollment: %w", err)
		}
		_, err = fmt.Fprintln(output, "Account disabled; browser sessions revoked. Shopping records preserved.")
		return err
	}
	owner, err := store.EnrollAccount(ctx, identity, sqlite.Preferences{Timezone: *timezone, Currency: *currency, PreferredRetailer: *retailer, WeekStart: *weekStart})
	if err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}
	_, err = fmt.Fprintln(output, "Account enrolled:", owner.ID())
	return err
}

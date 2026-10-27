// newsfeed-admin is an offline operator command, never an HTTP/MCP capability.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 || (args[0] != "enroll" && args[0] != "disable" && args[0] != "seed") {
		return fmt.Errorf("usage: newsfeed-admin enroll|disable|seed --db PATH --issuer EXACT_ISSUER --subject-file PRIVATE_FILE [--articles-file JSON_FILE]")
	}
	flags := flag.NewFlagSet("newsfeed-admin", flag.ContinueOnError)
	path := flags.String("db", "", "private persistent database path")
	issuer := flags.String("issuer", "", "exact verified issuer")
	subjectFile := flags.String("subject-file", "", "private file containing the verified subject")
	articlesFile := flags.String("articles-file", "", "bounded JSON array of seed requests (seed only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *issuer == "" || *subjectFile == "" {
		return fmt.Errorf("db, issuer and subject-file are required")
	}
	if (args[0] == "seed") != (*articlesFile != "") {
		return fmt.Errorf("articles-file is required for seed and allowed only for seed")
	}
	file, err := os.Open(*subjectFile)
	if err != nil {
		return fmt.Errorf("cannot open subject file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 256 {
		return fmt.Errorf("subject file must be a private regular file, at most 256 bytes")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(raw) > 256 {
		return fmt.Errorf("cannot read bounded subject")
	}
	// Permit exactly one terminal newline from an operator's text file.
	subject := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	identity := sqlite.ExternalIdentity{Issuer: *issuer, Subject: subject}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, *path, 2*time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	if args[0] == "disable" {
		return store.DisableAccount(ctx, identity)
	}
	if args[0] == "seed" {
		policy, err := thumbnail.NewPolicy(os.Getenv("NEWSFEED_THUMBNAIL_HOSTS"))
		if err != nil {
			return err
		}
		completed, err := seedArticles(ctx, store, identity, *articlesFile, policy)
		if err != nil {
			return err
		}
		fmt.Printf("Processed %d seed requests; retries preserve original outcomes.\n", completed)
		return nil
	}
	owner, err := store.EnrollAccount(ctx, identity)
	if err != nil {
		return err
	}
	fmt.Println(owner.ID())
	return nil
}

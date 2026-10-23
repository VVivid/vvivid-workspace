package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"agentshoppinglist/internal/buildinfo"
	"agentshoppinglist/internal/store/sqlite"
)

func recoveryCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && args[0] == "version" {
		return json.NewEncoder(output).Encode(buildinfo.Current())
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("db", "", "existing database for backup/inspect")
	archive := flags.String("archive", "", "backup tar file")
	target := flags.String("target", "", "new isolated restore database path")
	timeout := flags.Duration("timeout", 2*time.Minute, "bounded recovery operation")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *timeout <= 0 || *timeout > time.Hour {
		return fmt.Errorf("invalid recovery arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var result any
	var err error
	switch args[0] {
	case "backup":
		if *database == "" || *archive == "" || *target != "" {
			return fmt.Errorf("backup requires --db EXISTING --archive NEW.tar")
		}
		result, err = sqlite.CreateBackup(ctx, *database, *archive)
	case "verify":
		if *archive == "" || *database != "" || *target != "" {
			return fmt.Errorf("verify requires --archive EXISTING.tar")
		}
		result, err = sqlite.VerifyBackup(ctx, *archive)
	case "restore":
		if *archive == "" || *target == "" || *database != "" {
			return fmt.Errorf("restore requires --archive EXISTING.tar --target NEW-ISOLATED.sqlite")
		}
		result, err = sqlite.RestoreBackup(ctx, *archive, *target)
	case "inspect":
		if *database == "" || *archive != "" || *target != "" {
			return fmt.Errorf("inspect requires --db EXISTING.sqlite")
		}
		result, err = sqlite.InspectDatabase(ctx, *database)
	default:
		return fmt.Errorf("unknown recovery command")
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

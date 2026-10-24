// Package admincli implements local file-based administration. Possession of the
// server database key is the administrator boundary; it is never an HTTP API.
package admincli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/keyfile"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
)

func readFile(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("supplied file path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open supplied file")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("cannot read supplied file or size limit exceeded")
	}
	return raw, nil
}

func jsonFile(path string, limit int64, out any) error {
	raw, err := readFile(path, limit)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid supplied JSON file")
	}
	return nil
}

// Run returns safe errors without echoing supplied secret bytes or input bodies.
// No command creates keys, contacts live accounts, or starts a listener.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	global := flag.NewFlagSet("webhook-control-admin", flag.ContinueOnError)
	global.SetOutput(stderr)
	db := global.String("db", "", "server SQLite path")
	keyPath := global.String("key-file", "", "supplied raw 32-byte database encryption key file")
	actorIssuer := global.String("actor-issuer", "", "issuer namespace of the local administrator or operation actor")
	actorSubject := global.String("actor-subject", "", "subject of the local administrator or operation actor")
	if err := global.Parse(args); err != nil {
		return err
	}
	actor := management.Identity{Issuer: *actorIssuer, Subject: *actorSubject}
	if actor.PrincipalID() == "" || *db == "" || global.NArg() == 0 {
		return errors.New("db, key-file, actor-issuer, actor-subject and a command are required")
	}
	key, err := keyfile.Load(*keyPath)
	if err != nil {
		return err
	}
	defer clear(key)
	if len(key) != 32 {
		return errors.New("key-file must contain exactly 32 raw bytes")
	}
	command := global.Arg(0)
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "supplied grant, request or destination-policy JSON file")
	issuer := fs.String("issuer", "", "grant subject issuer namespace")
	subject := fs.String("subject", "", "grant subject")
	name := fs.String("name", "", "immutable secret name")
	version := fs.String("version", "", "immutable secret version")
	purpose := fs.String("purpose", "", "source-auth or http-header")
	valuePath := fs.String("value-file", "", "supplied raw secret value file")
	operation := fs.String("operation", "", "permitted management operation")
	policyPath := fs.String("policy-file", "", "server-owned origin rules JSON file for invoke")
	backupPath := fs.String("backup-file", "", "absolute SQLite backup path")
	destination := fs.String("destination", "", "absolute NEW destination; never overwritten")
	if err := fs.Parse(global.Args()[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected command arguments")
	}
	if command == "restore" {
		if err := sqlite.Restore(ctx, *backupPath, *db, key); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			Status string `json:"status"`
		}{"restored"})
	}
	s, err := sqlite.Open(*db, key)
	if err != nil {
		return err
	}
	defer s.Close()
	var out any
	switch command {
	case "backup":
		err = s.Backup(ctx, *destination)
		out = struct {
			Status string `json:"status"`
		}{"backed_up"}
	case "storage-stats":
		out, err = s.StorageUsage(ctx)
	case "grant-put":
		var g domain.Grant
		if err := jsonFile(*file, 256<<10, &g); err != nil {
			return err
		}
		principal := management.Identity{Issuer: *issuer, Subject: *subject}.PrincipalID()
		if principal == "" || g.PrincipalID != "" && g.PrincipalID != principal {
			return errors.New("explicit issuer and subject must match the supplied grant")
		}
		g.PrincipalID = principal
		if err := s.ProvisionGrant(ctx, actor.PrincipalID(), g); err != nil {
			return err
		}
		out = struct {
			ID domain.ID `json:"grant_id"`
		}{g.ID}
	case "secret-put":
		value, err := readFile(*valuePath, 4096)
		if err != nil {
			return err
		}
		defer clear(value)
		ref := domain.SecretRef{Name: *name, Version: *version}
		if err := s.PutSecret(ctx, actor.PrincipalID(), ref, domain.SecretPurpose(*purpose), value); err != nil {
			return err
		}
		out = struct {
			Ref     domain.SecretRef     `json:"ref"`
			Purpose domain.SecretPurpose `json:"purpose"`
		}{ref, domain.SecretPurpose(*purpose)}
	case "secrets-list":
		out, err = s.SecretMetadata(ctx)
	case "grants-list":
		out, err = s.GrantMetadata(ctx)
	case "audit-list":
		out, err = s.AuditMetadata(ctx, 100)
	case "invoke":
		var rules []httptransport.OriginRule
		if err := jsonFile(*policyPath, 256<<10, &rules); err != nil {
			return err
		}
		policy, err := httptransport.NewPolicy(rules)
		if err != nil {
			return err
		}
		service, err := management.New(s, policy, time.Now)
		if err != nil {
			return err
		}
		raw, err := readFile(*file, 2<<20)
		if err != nil {
			return err
		}
		out, err = service.Execute(ctx, actor, *operation, raw)
		if err != nil {
			return err
		}
	default:
		return errors.New("command must be grant-put, secret-put, secrets-list, grants-list, audit-list, invoke, backup, restore or storage-stats")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(out)
}

package sqlite

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func TestSecretsEncryptedImmutableAcrossRestartAndPurposeScoped(t *testing.T) {
	s, clock, path, key := openTest(t)
	ref := domain.SecretRef{Name: "synthetic-inbound", Version: "v1"}
	value := []byte("synthetic-source-secret-never-plaintext-db-0fa9e7f63")
	if err := s.PutSecret(t.Context(), "local-admin", ref, domain.SecretSourceAuth, value); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecret(t.Context(), "local-admin", ref, domain.SecretSourceAuth, value); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		purpose domain.SecretPurpose
		value   []byte
	}{{domain.SecretHTTPHeader, value}, {domain.SecretSourceAuth, []byte("changed-synthetic-bytes")}} {
		if err := s.PutSecret(t.Context(), "local-admin", ref, item.purpose, item.value); !errors.Is(err, ports.ErrImmutable) {
			t.Fatalf("immutability: %v", err)
		}
	}
	metadata, err := s.SecretMetadata(t.Context())
	if err != nil || len(metadata) != 1 || metadata[0].Purpose != domain.SecretSourceAuth {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	if _, err := s.SecretsFor(domain.SecretHTTPHeader).ReadSecret(t.Context(), ref); !errors.Is(err, ports.ErrSecretUnavailable) {
		t.Fatalf("exported source auth credential: %v", err)
	}
	got, err := s.SecretsFor(domain.SecretSourceAuth).ReadSecret(t.Context(), ref)
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("source read=%q err=%v", got, err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(raw, value) {
			t.Fatal("secret value present in SQLite pages")
		}
	}
	// A second Store models restart while retaining the original fixture cleanup.
	reopened, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.PutSecret(t.Context(), "local-admin", ref, domain.SecretSourceAuth, []byte("changed-after-restart")); !errors.Is(err, ports.ErrImmutable) {
		t.Fatalf("restart immutability: %v", err)
	}
	if _, err := reopened.db.ExecContext(t.Context(), "UPDATE secrets SET purpose='http-header' WHERE name=?", ref.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.SecretsFor(domain.SecretHTTPHeader).ReadSecret(t.Context(), ref); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("purpose metadata not authenticated: %v", err)
	}
}

func TestManagementMutationAndAuditAreOneTransaction(t *testing.T) {
	s, _, _, _ := openTest(t)
	config := domain.Configuration{Source: domain.Source{ID: "source", Revision: 1, Name: "immutable"}}
	err := s.Manage(t.Context(), func(tx ports.ManagementTx) error {
		if _, err := tx.PutConfiguration("source", 0, config); err != nil {
			return err
		}
		return tx.Audit(domain.Audit{ID: "bad-audit", At: time.Time{}, PrincipalID: "admin", Operation: "config_put"})
	})
	if err == nil {
		t.Fatal("invalid audit committed")
	}
	for _, table := range []string{"configuration_snapshots", "configuration_heads", "resource_versions", "audit"} {
		if tableCount(t, s, table) != 0 {
			t.Fatalf("partial commit in %s", table)
		}
	}
	if err := s.Manage(t.Context(), func(tx ports.ManagementTx) error {
		if _, err := tx.PutConfiguration("source", 0, config); err != nil {
			return err
		}
		return tx.Audit(domain.Audit{ID: "good-audit", At: baseTime, PrincipalID: "admin", Operation: "config_put", SourceID: "source", Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	config.Source.Name = "changed"
	if err := s.Manage(t.Context(), func(tx ports.ManagementTx) error { _, err := tx.PutConfiguration("source", 1, config); return err }); !errors.Is(err, ports.ErrImmutable) {
		t.Fatalf("resource change accepted: %v", err)
	}
	if tableCount(t, s, "configuration_snapshots") != 1 || tableCount(t, s, "audit") != 1 {
		t.Fatal("failed configuration altered history")
	}
}

func TestSourceOwnershipIsStableAcrossResourceRevisions(t *testing.T) {
	s, _, path, key := openTest(t)
	first := domain.Configuration{Source: domain.Source{ID: "source-a", Revision: 1}, Templates: []domain.Template{{ID: "shared-template", Revision: 1, SourceID: "source-a", Fields: map[string]string{"message": "event.message"}}}}
	if err := s.Manage(t.Context(), func(tx ports.ManagementTx) error { _, err := tx.PutConfiguration("source-a", 0, first); return err }); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, key, WithClock(func() time.Time { return baseTime }))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	first.Templates[0].Fields["message"] = "'changed'"
	if err := reopened.Manage(t.Context(), func(tx ports.ManagementTx) error { _, err := tx.PutConfiguration("source-a", 1, first); return err }); !errors.Is(err, ports.ErrImmutable) {
		t.Fatalf("restart resource change: %v", err)
	}
	second := domain.Configuration{Source: domain.Source{ID: "source-b", Revision: 1}, Templates: []domain.Template{{ID: "shared-template", Revision: 2, SourceID: "source-b", Fields: map[string]string{}}}}
	if err := reopened.Manage(t.Context(), func(tx ports.ManagementTx) error { _, err := tx.PutConfiguration("source-b", 0, second); return err }); !errors.Is(err, ports.ErrImmutable) {
		t.Fatalf("changed resource ownership: %v", err)
	}
}

func TestManagementReceiptDigestAndRetrySurviveRestart(t *testing.T) {
	s, clock, path, key := openTest(t)
	principal := `["https://issuer.example","synthetic"]`
	if err := s.Manage(t.Context(), func(tx ports.ManagementTx) error {
		return tx.SaveReceipt(principal, "events_replay", "source", digest("request-key"), digest("selection"), ports.ManagementReceipt{EventID: "event-replay", DeliveryIDs: []domain.ID{"delivery-replay"}})
	}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRowContext(t.Context(), "SELECT key_hash FROM management_receipts").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == digest("request-key") {
		t.Fatal("management receipt stored an unkeyed request digest")
	}
	reopened, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Manage(t.Context(), func(tx ports.ManagementTx) error {
		out, err := tx.Receipt(principal, "events_replay", "source", digest("request-key"), digest("selection"))
		if err == nil && (!out.Duplicate || out.EventID != "event-replay" || len(out.DeliveryIDs) != 1) {
			t.Fatalf("receipt=%+v", out)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

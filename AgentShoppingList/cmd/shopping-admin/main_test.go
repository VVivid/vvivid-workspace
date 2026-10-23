package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentshoppinglist/internal/store/sqlite"
)

func TestExplicitEnrollmentAndDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shopping.sqlite")
	subjectPath := filepath.Join(t.TempDir(), "subject")
	subject := rand.Text()
	if err := os.WriteFile(subjectPath, []byte(subject+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"enroll", "--db", path, "--issuer", "https://issuer.example/realm", "--subject-file", subjectPath}
	if err := run(t.Context(), base, &bytes.Buffer{}); err == nil {
		t.Fatal("enrollment inferred missing preferences")
	}
	args := append(base, "--timezone", "America/New_York", "--currency", "USD", "--retailer", "walmart", "--week-start", "sunday")
	var output bytes.Buffer
	if err := run(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte(subject)) {
		t.Fatal("operator output exposed the external subject")
	}
	if err := run(t.Context(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate enrollment silently created another owner")
	}
	store, err := sqlite.Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	identity := sqlite.ExternalIdentity{Issuer: "https://issuer.example/realm", Subject: subject}
	owner, err := store.OwnerForIdentity(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := store.Preferences(t.Context(), owner)
	if err != nil || prefs.Timezone != "America/New_York" || prefs.WeekStart != "sunday" || prefs.Currency != "USD" || prefs.PreferredRetailer != "walmart" {
		t.Fatalf("explicit preferences not preserved: %v", err)
	}
	disable := append([]string{"disable"}, base[1:]...)
	if err := run(t.Context(), disable, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OwnerForIdentity(t.Context(), identity); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("disabled owner resolved: %v", err)
	}
	if _, err := store.Write(t.Context(), owner, sqlite.Mutation{Key: "after-disable", Operation: "after-disable", Payload: []byte(`{}`), RequestID: "test", ClientID: "test"}, func(*sqlite.Tx) ([]byte, error) { return []byte(`{}`), nil }); !errors.Is(err, sqlite.ErrOwnerRequired) {
		t.Fatalf("previous owner handle wrote after disable: %v", err)
	}
	if _, err := store.Preferences(t.Context(), owner); err != nil {
		t.Fatal("disable deleted shopping records")
	}
}

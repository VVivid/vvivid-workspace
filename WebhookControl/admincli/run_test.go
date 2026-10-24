package admincli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
)

func TestSuppliedFileAdministrationAndSharedOperationAuthorization(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, raw []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	keyPath := write("supplied-synthetic.key", bytes.Repeat([]byte{9}, 32))
	value := []byte("synthetic-credential-for-temp-test-only")
	valuePath := write("supplied-synthetic-secret", value)
	actor := management.Identity{Issuer: "https://issuer.example", Subject: "synthetic-admin"}
	base := []string{"-db", filepath.Join(dir, "state.db"), "-key-file", keyPath, "-actor-issuer", actor.Issuer, "-actor-subject", actor.Subject}
	run := func(command ...string) (string, error) {
		t.Helper()
		var out, errs bytes.Buffer
		err := Run(t.Context(), append(slices.Clone(base), command...), &out, &errs)
		if bytes.Contains(out.Bytes(), value) || bytes.Contains(errs.Bytes(), value) {
			t.Fatal("CLI exposed secret value")
		}
		return out.String(), err
	}
	if _, err := run("secret-put", "-name", "inbound", "-version", "1", "-purpose", "source-auth", "-value-file", valuePath); err != nil {
		t.Fatal(err)
	}
	metadata, err := run("secrets-list")
	if err != nil || !strings.Contains(metadata, "source-auth") || strings.Contains(metadata, "ciphertext") {
		t.Fatalf("metadata=%s err=%v", metadata, err)
	}
	changed := write("supplied-changed-secret", []byte("different-ephemeral-secret"))
	if _, err := run("secret-put", "-name", "inbound", "-version", "1", "-purpose", "source-auth", "-value-file", changed); err == nil {
		t.Fatal("CLI changed an immutable secret")
	}
	grant := domain.Grant{ID: "grant", SourceIDs: []domain.ID{"source"}, Actions: []domain.Action{domain.ActionReadConfig}}
	raw, _ := json.Marshal(grant)
	grantPath := write("supplied-grant.json", raw)
	if _, err := run("grant-put", "-issuer", actor.Issuer, "-subject", actor.Subject, "-file", grantPath); err != nil {
		t.Fatal(err)
	}
	policyPath := write("supplied-policy.json", []byte(`[]`))
	requestPath := write("supplied-request.json", []byte(`{"source_id":"source","input":{}}`))
	if _, err := run("invoke", "-policy-file", policyPath, "-operation", "config_get", "-file", requestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := run("invoke", "-policy-file", policyPath, "-operation", "events_list", "-file", requestPath); err == nil {
		t.Fatal("CLI operation bypassed action grant")
	}
	base[len(base)-1] = "ungranted-subject"
	if _, err := run("invoke", "-policy-file", policyPath, "-operation", "config_get", "-file", requestPath); err == nil {
		t.Fatal("CLI invoke bypassed principal grant")
	}
}

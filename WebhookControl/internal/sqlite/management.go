package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

var _ ports.ManagementStore = (*Store)(nil)
var _ ports.ConfigurationStore = (*Store)(nil)
var _ ports.SecretReader = (*Store)(nil)
var _ ports.LocalAdministrator = (*Store)(nil)

func randomID() domain.ID { return domain.ID(rand.Text()) }

const managementSchema = `
CREATE TABLE configuration_heads(source_id TEXT PRIMARY KEY, latest INTEGER NOT NULL, draft INTEGER NOT NULL, active INTEGER NOT NULL);
CREATE TABLE configuration_snapshots(source_id TEXT NOT NULL, revision INTEGER NOT NULL, selected_from INTEGER NOT NULL, configuration TEXT NOT NULL, PRIMARY KEY(source_id, revision));
CREATE TABLE resource_versions(kind TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL, source_id TEXT NOT NULL, content TEXT NOT NULL, PRIMARY KEY(kind, id, revision));
CREATE TABLE grants(id TEXT PRIMARY KEY, principal_id TEXT NOT NULL, content TEXT NOT NULL);
CREATE INDEX grants_principal ON grants(principal_id);
CREATE TABLE secrets(name TEXT NOT NULL, version TEXT NOT NULL, purpose TEXT NOT NULL, ciphertext BLOB NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(name, version));
CREATE TABLE audit(id TEXT PRIMARY KEY, at INTEGER NOT NULL, principal_id TEXT NOT NULL, operation TEXT NOT NULL, source_id TEXT NOT NULL, resource_id TEXT NOT NULL, revision INTEGER NOT NULL);
CREATE TABLE management_receipts(principal_id TEXT NOT NULL, operation TEXT NOT NULL, source_id TEXT NOT NULL, key_hash TEXT NOT NULL, fingerprint TEXT NOT NULL, result TEXT NOT NULL, PRIMARY KEY(principal_id, operation, source_id, key_hash));
ALTER TABLE events ADD COLUMN replay_of TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN retry_of TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN frozen_refs TEXT NOT NULL DEFAULT '{}';
CREATE INDEX events_source_page ON events(source_id, id);
CREATE INDEX deliveries_source_page ON deliveries(source_id, id);
PRAGMA user_version = 3;
`

func (s *Store) migrateManagement(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, managementSchema); err != nil {
		return dbError(err)
	}
	// Preserve safe revision metadata for old retained ciphertext. Already-deleted
	// pre-v3 payloads have no recoverable refs; their metadata remains queryable.
	rows, err := tx.QueryContext(ctx, "SELECT delivery_id, ciphertext FROM delivery_payloads")
	if err != nil {
		return dbError(err)
	}
	refs := map[string][]byte{}
	for rows.Next() {
		var id string
		var ciphertext []byte
		if err := rows.Scan(&id, &ciphertext); err != nil {
			rows.Close()
			return dbError(err)
		}
		raw, err := s.decrypt("delivery", id, ciphertext)
		if err != nil {
			rows.Close()
			return err
		}
		var frozen domain.FrozenDelivery
		if json.Unmarshal(raw, &frozen) != nil {
			rows.Close()
			return ErrCiphertext
		}
		refs[id], _ = json.Marshal(frozenStatus(frozen))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dbError(err)
	}
	rows.Close()
	for id, raw := range refs {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET frozen_refs = ? WHERE id = ?", string(raw), id); err != nil {
			return dbError(err)
		}
	}
	return nil
}

type managementTx struct {
	s      *Store
	tx     *sql.Tx
	ctx    context.Context
	notify bool
}

func (s *Store) Manage(ctx context.Context, run func(ports.ManagementTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	m := &managementTx{s: s, tx: tx, ctx: ctx}
	if err := run(m); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	if m.notify {
		s.signal()
	}
	return nil
}

func (m *managementTx) Head(source domain.ID) (domain.ConfigurationHead, error) {
	var h domain.ConfigurationHead
	err := m.tx.QueryRowContext(m.ctx, "SELECT latest, draft, active FROM configuration_heads WHERE source_id = ?", source).Scan(&h.Latest, &h.Draft, &h.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return h, nil
	}
	return h, dbError(err)
}

func (m *managementTx) Configuration(source domain.ID, revision domain.Revision) (domain.ConfigurationSnapshot, error) {
	var out domain.ConfigurationSnapshot
	var raw []byte
	err := m.tx.QueryRowContext(m.ctx, "SELECT revision, selected_from, configuration FROM configuration_snapshots WHERE source_id = ? AND revision = ?", source, revision).Scan(&out.Revision, &out.SelectedFrom, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if err != nil {
		return out, dbError(err)
	}
	if err := json.Unmarshal(raw, &out.Configuration); err != nil {
		return out, dbError(err)
	}
	return out, nil
}

func (m *managementTx) SourceRevision(source domain.ID, revision domain.Revision) (domain.Source, error) {
	var out domain.Source
	var raw []byte
	err := m.tx.QueryRowContext(m.ctx, "SELECT content FROM resource_versions WHERE kind='source' AND id=? AND revision=? AND source_id=?", source, revision, source).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if err != nil {
		return out, dbError(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, dbError(err)
	}
	return out, nil
}

func (s *Store) ActiveConfiguration(ctx context.Context, source domain.ID) (domain.Configuration, error) {
	var config domain.Configuration
	err := s.Manage(ctx, func(tx ports.ManagementTx) error {
		h, err := tx.Head(source)
		if err != nil {
			return err
		}
		v, err := tx.Configuration(source, h.Active)
		config = v.Configuration
		return err
	})
	return config, err
}

func (m *managementTx) PutConfiguration(source domain.ID, expected domain.Revision, config domain.Configuration) (domain.ConfigurationSnapshot, error) {
	h, err := m.Head(source)
	if err != nil {
		return domain.ConfigurationSnapshot{}, err
	}
	if h.Latest != expected {
		return domain.ConfigurationSnapshot{}, ports.ErrStaleRevision
	}
	if source != config.Source.ID || h.Latest >= math.MaxInt64 {
		return domain.ConfigurationSnapshot{}, ErrInvalid
	}
	if err := m.immutableResources(config); err != nil {
		return domain.ConfigurationSnapshot{}, err
	}
	out := domain.ConfigurationSnapshot{Revision: h.Latest + 1, Configuration: config}
	h.Latest, h.Draft = out.Revision, out.Revision
	return out, m.saveConfiguration(source, h, out)
}

func (m *managementTx) SelectConfiguration(source domain.ID, expected, selected domain.Revision, rollback bool) (domain.ConfigurationSnapshot, error) {
	h, err := m.Head(source)
	if err != nil {
		return domain.ConfigurationSnapshot{}, err
	}
	if h.Active != expected || !rollback && h.Draft != selected {
		return domain.ConfigurationSnapshot{}, ports.ErrStaleRevision
	}
	if h.Latest >= math.MaxInt64 {
		return domain.ConfigurationSnapshot{}, ErrInvalid
	}
	out, err := m.Configuration(source, selected)
	if err != nil {
		return out, err
	}
	out.SelectedFrom, out.Revision = selected, h.Latest+1
	h.Latest, h.Active = out.Revision, out.Revision
	return out, m.saveConfiguration(source, h, out)
}

func (m *managementTx) saveConfiguration(source domain.ID, h domain.ConfigurationHead, out domain.ConfigurationSnapshot) error {
	raw, err := json.Marshal(out.Configuration)
	if err != nil {
		return ErrInvalid
	}
	if _, err := m.tx.ExecContext(m.ctx, "INSERT INTO configuration_snapshots VALUES (?, ?, ?, ?)", source, out.Revision, out.SelectedFrom, string(raw)); err != nil {
		return dbError(err)
	}
	_, err = m.tx.ExecContext(m.ctx, "INSERT INTO configuration_heads VALUES (?, ?, ?, ?) ON CONFLICT(source_id) DO UPDATE SET latest=excluded.latest, draft=excluded.draft, active=excluded.active", source, h.Latest, h.Draft, h.Active)
	return dbError(err)
}

func (m *managementTx) immutableResources(c domain.Configuration) error {
	check := func(kind string, id domain.ID, rev domain.Revision, value any) error {
		if !validID(string(id)) || rev == 0 || rev > math.MaxInt64 {
			return ErrInvalid
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return ErrInvalid
		}
		var owner domain.ID
		err = m.tx.QueryRowContext(m.ctx, "SELECT source_id FROM resource_versions WHERE kind=? AND id=? LIMIT 1", kind, id).Scan(&owner)
		if err == nil && owner != c.Source.ID {
			return ports.ErrImmutable
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return dbError(err)
		}
		var old []byte
		err = m.tx.QueryRowContext(m.ctx, "SELECT content FROM resource_versions WHERE kind=? AND id=? AND revision=?", kind, id, rev).Scan(&old)
		if err == nil {
			if !bytes.Equal(old, raw) {
				return ports.ErrImmutable
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return dbError(err)
		}
		_, err = m.tx.ExecContext(m.ctx, "INSERT INTO resource_versions VALUES (?, ?, ?, ?, ?)", kind, id, rev, c.Source.ID, string(raw))
		return dbError(err)
	}
	if err := check("source", c.Source.ID, c.Source.Revision, c.Source); err != nil {
		return err
	}
	for _, x := range c.Rules {
		if err := check("rule", x.ID, x.Revision, x); err != nil {
			return err
		}
	}
	for _, x := range c.Templates {
		if err := check("template", x.ID, x.Revision, x); err != nil {
			return err
		}
	}
	for _, x := range c.Routes {
		if err := check("route", x.ID, x.Revision, x); err != nil {
			return err
		}
	}
	for _, x := range c.Transporters {
		if err := check("transporter", x.ID, x.Revision, x); err != nil {
			return err
		}
	}
	return nil
}

func (m *managementTx) Grants(principal string) ([]domain.Grant, error) {
	rows, err := m.tx.QueryContext(m.ctx, "SELECT content FROM grants WHERE principal_id=? ORDER BY id", principal)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []domain.Grant{}
	for rows.Next() {
		var raw []byte
		var g domain.Grant
		if err := rows.Scan(&raw); err != nil {
			return nil, dbError(err)
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, dbError(err)
		}
		out = append(out, g)
	}
	return out, dbError(rows.Err())
}

func secretID(ref domain.SecretRef, purpose domain.SecretPurpose) string {
	raw, _ := json.Marshal(struct {
		Ref     domain.SecretRef
		Purpose domain.SecretPurpose
	}{ref, purpose})
	return string(raw)
}

func (m *managementTx) Secret(ref domain.SecretRef) ([]byte, error) {
	var ciphertext []byte
	var purpose domain.SecretPurpose
	err := m.tx.QueryRowContext(m.ctx, "SELECT purpose,ciphertext FROM secrets WHERE name=? AND version=?", ref.Name, ref.Version).Scan(&purpose, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrSecretUnavailable
	}
	if err != nil {
		return nil, dbError(err)
	}
	return m.s.decrypt("secret", secretID(ref, purpose), ciphertext)
}

func (s *Store) ReadSecret(ctx context.Context, ref domain.SecretRef) ([]byte, error) {
	var out []byte
	err := s.Manage(ctx, func(tx ports.ManagementTx) error {
		var err error
		out, err = tx.Secret(ref)
		return err
	})
	return out, err
}

func (m *managementTx) SecretPurpose(ref domain.SecretRef) (domain.SecretPurpose, error) {
	var purpose domain.SecretPurpose
	err := m.tx.QueryRowContext(m.ctx, "SELECT purpose FROM secrets WHERE name=? AND version=?", ref.Name, ref.Version).Scan(&purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return purpose, ports.ErrSecretUnavailable
	}
	return purpose, dbError(err)
}

type purposeReader struct {
	store   *Store
	purpose domain.SecretPurpose
}

// SecretsFor gives ingress or the HTTP Transporter an immutable purpose-scoped
// view. An inbound authentication credential can never be exported as a header.
func (s *Store) SecretsFor(purpose domain.SecretPurpose) ports.SecretReader {
	return purposeReader{s, purpose}
}

func (r purposeReader) ReadSecret(ctx context.Context, ref domain.SecretRef) ([]byte, error) {
	var value []byte
	err := r.store.Manage(ctx, func(tx ports.ManagementTx) error {
		purpose, err := tx.SecretPurpose(ref)
		if err != nil {
			return err
		}
		if purpose != r.purpose {
			return ports.ErrSecretUnavailable
		}
		value, err = tx.Secret(ref)
		return err
	})
	return value, err
}

func (m *managementTx) Audit(a domain.Audit) error {
	if !validID(string(a.ID)) || !validTime(a.At) || a.PrincipalID == "" || a.Operation == "" {
		return ErrInvalid
	}
	_, err := m.tx.ExecContext(m.ctx, "INSERT INTO audit VALUES (?, ?, ?, ?, ?, ?, ?)", a.ID, a.At.UnixNano(), a.PrincipalID, a.Operation, a.SourceID, a.ResourceID, a.Revision)
	return dbError(err)
}

// ProvisionGrant and PutSecret are local-only capabilities. Neither is in the
// operation registry or ManagementTx interface exposed to the shared service.
func (s *Store) ProvisionGrant(ctx context.Context, actor string, g domain.Grant) error {
	if actor == "" || !validID(string(g.ID)) || g.PrincipalID == "" || g.OriginalRetention.MaxSeconds < 0 || g.DeliveryRetention.MaxSeconds < 0 {
		return ErrInvalid
	}
	var principal []string
	if json.Unmarshal([]byte(g.PrincipalID), &principal) != nil || len(principal) != 2 || principal[0] == "" || principal[1] == "" {
		return ErrInvalid
	}
	canonical, _ := json.Marshal(principal)
	if string(canonical) != g.PrincipalID || g.ExpiresAt != nil && !validTime(*g.ExpiresAt) {
		return ErrInvalid
	}
	for _, source := range g.SourceIDs {
		if !validID(string(source)) {
			return ErrInvalid
		}
	}
	for _, name := range g.SecretNames {
		if !validID(name) {
			return ErrInvalid
		}
	}
	for _, action := range g.Actions {
		switch action {
		case domain.ActionReadConfig, domain.ActionWriteConfig, domain.ActionActivateConfig, domain.ActionPreview, domain.ActionReadEvents, domain.ActionReplayEvents, domain.ActionReadDeliveries, domain.ActionRetryDelivery:
		default:
			return ErrInvalid
		}
	}
	raw, err := json.Marshal(g)
	if err != nil || len(raw) > 256<<10 {
		return ErrInvalid
	}
	return s.Manage(ctx, func(tx ports.ManagementTx) error {
		m := tx.(*managementTx)
		if _, err := m.tx.ExecContext(ctx, "INSERT INTO grants VALUES (?, ?, ?) ON CONFLICT(id) DO UPDATE SET principal_id=excluded.principal_id, content=excluded.content", g.ID, g.PrincipalID, string(raw)); err != nil {
			return dbError(err)
		}
		return tx.Audit(domain.Audit{ID: randomID(), At: s.now(), PrincipalID: actor, Operation: "admin.grant", ResourceID: g.ID})
	})
}

func (s *Store) PutSecret(ctx context.Context, actor string, ref domain.SecretRef, purpose domain.SecretPurpose, value []byte) error {
	if actor == "" || !validID(ref.Name) || !validID(ref.Version) || len(value) == 0 || len(value) > 4096 || purpose != domain.SecretSourceAuth && purpose != domain.SecretHTTPHeader {
		return ErrInvalid
	}
	return s.Manage(ctx, func(tx ports.ManagementTx) error {
		old, err := tx.Secret(ref)
		if err == nil {
			oldPurpose, err := tx.SecretPurpose(ref)
			if err != nil {
				return err
			}
			if !bytes.Equal(old, value) || oldPurpose != purpose {
				return ports.ErrImmutable
			}
			return nil
		}
		if !errors.Is(err, ports.ErrSecretUnavailable) {
			return err
		}
		m := tx.(*managementTx)
		if _, err := m.tx.ExecContext(ctx, "INSERT INTO secrets VALUES (?, ?, ?, ?, ?)", ref.Name, ref.Version, purpose, s.encrypt("secret", secretID(ref, purpose), value), s.now().UnixNano()); err != nil {
			return dbError(err)
		}
		return tx.Audit(domain.Audit{ID: randomID(), At: s.now(), PrincipalID: actor, Operation: "admin.secret", ResourceID: domain.ID(ref.Name)})
	})
}

func (s *Store) SecretMetadata(ctx context.Context) ([]domain.SecretMetadata, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name, version, purpose, created_at FROM secrets ORDER BY name, version")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []domain.SecretMetadata{}
	for rows.Next() {
		var v domain.SecretMetadata
		var at int64
		if err := rows.Scan(&v.Ref.Name, &v.Ref.Version, &v.Purpose, &at); err != nil {
			return nil, dbError(err)
		}
		v.CreatedAt = time.Unix(0, at).UTC()
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

func (s *Store) GrantMetadata(ctx context.Context) ([]domain.Grant, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT content FROM grants ORDER BY id")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []domain.Grant{}
	for rows.Next() {
		var raw []byte
		var g domain.Grant
		if err := rows.Scan(&raw); err != nil {
			return nil, dbError(err)
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, dbError(err)
		}
		out = append(out, g)
	}
	return out, dbError(rows.Err())
}

func (s *Store) AuditMetadata(ctx context.Context, limit int) ([]domain.Audit, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, at, principal_id, operation, source_id, resource_id, revision FROM audit ORDER BY at DESC, id DESC LIMIT ?", min(max(limit, 1), 1000))
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []domain.Audit{}
	for rows.Next() {
		var a domain.Audit
		var at int64
		if err := rows.Scan(&a.ID, &at, &a.PrincipalID, &a.Operation, &a.SourceID, &a.ResourceID, &a.Revision); err != nil {
			return nil, dbError(err)
		}
		a.At = time.Unix(0, at).UTC()
		out = append(out, a)
	}
	return out, dbError(rows.Err())
}

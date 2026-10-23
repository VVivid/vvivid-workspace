package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // Keep per-account IANA timezones available in minimal images.
	"unicode"
)

// Owner is an opaque enrolled account handle. Resolve it only after verifying a
// session or token; an external resource ID cannot construct an owner.
type Owner struct{ id string }

func (o Owner) ID() string { return o.id }

type ExternalIdentity struct {
	Issuer  string
	Subject string
}

type Preferences struct {
	Timezone          string `json:"timezone"`
	Currency          string `json:"currency"`
	PreferredRetailer string `json:"preferred_retailer"`
	WeekStart         string `json:"week_start"`
	Revision          int64  `json:"revision"`
}

// EnrollAccount is an explicit provisioning operation, not a login fallback.
// No HTTP or MCP route calls it. The local operator CLI supplies the policy and
// explicit preferences; OwnerForIdentity never creates an account.
func (s *Store) EnrollAccount(ctx context.Context, identity ExternalIdentity, preferences Preferences) (Owner, error) {
	if err := identity.validate(); err != nil {
		return Owner{}, err
	}
	if err := preferences.validate(); err != nil {
		return Owner{}, err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return Owner{}, err
	}
	defer tx.Rollback()
	owner := Owner{id: rand.Text()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id, created_at) VALUES (?, ?)", owner.id, time.Now().UTC().UnixMilli()); err != nil {
		return Owner{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO identities(issuer, subject, owner_id) VALUES (?, ?, ?)", identity.Issuer, identity.Subject, owner.id); err != nil {
		return Owner{}, fmt.Errorf("enroll identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO preferences(owner_id, timezone, currency, preferred_retailer, week_start)
		VALUES (?, ?, ?, ?, ?)`, owner.id, preferences.Timezone, preferences.Currency, preferences.PreferredRetailer, preferences.WeekStart); err != nil {
		return Owner{}, err
	}
	if err := tx.Commit(); err != nil {
		return Owner{}, err
	}
	return owner, nil
}

func (s *Store) OwnerForIdentity(ctx context.Context, identity ExternalIdentity) (Owner, error) {
	if err := identity.validate(); err != nil {
		return Owner{}, err
	}
	var owner Owner
	err := s.read.QueryRowContext(ctx, `SELECT i.owner_id FROM identities i JOIN accounts a ON a.id = i.owner_id
		WHERE i.issuer = ? AND i.subject = ? AND a.disabled_at IS NULL`, identity.Issuer, identity.Subject).Scan(&owner.id)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	return owner, err
}

func (s *Store) Preferences(ctx context.Context, owner Owner) (Preferences, error) {
	if owner.id == "" {
		return Preferences{}, ErrOwnerRequired
	}
	var preferences Preferences
	err := s.read.QueryRowContext(ctx, `SELECT timezone, currency, preferred_retailer, week_start, revision
		FROM preferences WHERE owner_id = ?`, owner.id).
		Scan(&preferences.Timezone, &preferences.Currency, &preferences.PreferredRetailer, &preferences.WeekStart, &preferences.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{}, ErrNotFound
	}
	return preferences, err
}

func (tx *Tx) SetPreferences(preferences Preferences, expectedRevision int64) error {
	if err := preferences.validate(); err != nil {
		return err
	}
	if expectedRevision < 1 {
		return fmt.Errorf("expected preference revision must be positive")
	}
	result, err := tx.sql.ExecContext(tx.ctx, `UPDATE preferences SET timezone = ?, currency = ?, preferred_retailer = ?, week_start = ?, revision = revision + 1
		WHERE owner_id = ? AND revision = ?`, preferences.Timezone, preferences.Currency, preferences.PreferredRetailer, preferences.WeekStart, tx.owner.id, expectedRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	tx.affected = append(tx.affected, affectedRecord{Kind: "preferences", ID: tx.owner.id})
	return nil
}

func (identity ExternalIdentity) validate() error {
	issuer, err := url.Parse(identity.Issuer)
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("identity issuer must be an absolute HTTP(S) issuer URL")
	}
	loopback := issuer.Hostname() == "localhost"
	if ip := net.ParseIP(issuer.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if issuer.Scheme != "https" && !(issuer.Scheme == "http" && loopback) {
		return fmt.Errorf("identity issuer must use HTTPS (HTTP is permitted only on loopback)")
	}
	if len(identity.Subject) < 1 || len(identity.Subject) > 255 || strings.TrimSpace(identity.Subject) != identity.Subject {
		return fmt.Errorf("identity subject must contain 1–255 characters without surrounding whitespace")
	}
	return nil
}

func (preferences Preferences) validate() error {
	if preferences.Timezone == "" || preferences.Timezone == "Local" {
		return fmt.Errorf("an explicit timezone is required")
	}
	if _, err := time.LoadLocation(preferences.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q", preferences.Timezone)
	}
	if !ValidCurrency(preferences.Currency) {
		return fmt.Errorf("currency must be a supported ISO 4217 code")
	}
	switch preferences.WeekStart {
	case "sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday":
	default:
		return fmt.Errorf("an explicit week start is required")
	}
	if strings.TrimSpace(preferences.PreferredRetailer) == "" || strings.TrimSpace(preferences.PreferredRetailer) != preferences.PreferredRetailer ||
		len(preferences.PreferredRetailer) > 128 || strings.ContainsFunc(preferences.PreferredRetailer, unicode.IsControl) {
		return fmt.Errorf("preferred retailer must contain 1–128 characters")
	}
	return nil
}

func (preferences Preferences) Validate() error { return preferences.validate() }

func ValidCurrency(value string) bool {
	return len(value) == 3 && strings.Contains(currencyCodes, " "+value+" ")
}

// Codes are data validation, not an exchange-rate or decimal-place definition.
// Include historical codes so changing currency policy cannot strand an account.
const currencyCodes = " AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB BRL BSD BTN BWP BYN BZD CAD CDF CHF CLP CNY COP CRC CUP CVE CZK DJF DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD GNF GTQ GYD HKD HNL HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW KRW KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD OMR PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE SLL SOS SRD SSP STN SVC SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD UYU UZS VED VES VND VUV WST XAF XCD XCG XOF XPF YER ZAR ZMW ZWG ZWL "

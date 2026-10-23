package collector

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStorageDiagnosticsKeepSQLAndRequestDetailsPrivate(t *testing.T) {
	s := testStore(t)
	_, raw := s.db.ExecContext(t.Context(), "SELECT private_health_value FROM private_missing_table")
	if raw == nil {
		t.Fatal("expected a synthetic SQLite error")
	}
	err := storageError(raw)
	httpStatus, sqliteCode := DiagnosticCodes(err)
	if Code(err) != "storage_error" || err.Error() != "storage_error" || sqliteCode != 1 || httpStatus != 0 {
		t.Fatal("SQLite diagnostics lost the stable application code or numeric driver code")
	}
	result := Result{Status: "failed", ErrorCode: Code(err), SQLiteCode: sqliteCode}
	output, encodeErr := json.Marshal(result)
	if encodeErr != nil || strings.Contains(string(output), "private_") || !strings.Contains(string(output), `"sqlite_code":1`) {
		t.Fatal("diagnostic output exposed SQL text or omitted the numeric code")
	}
	status, code := DiagnosticCodes(&Error{Code: "api_error", Status: 503})
	if status != 503 || code != 0 {
		t.Fatal("HTTP status diagnostics were not retained")
	}
	_, code = DiagnosticCodes(storageError(errors.New("synthetic secret")))
	if code != 0 {
		t.Fatal("non-SQLite errors acquired a driver code")
	}
}

func TestRunReportsNumericFailureCodes(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		e, api := fixture(t)
		api.page = func(Metric, Window, string) (Page, error) {
			return Page{}, &Error{Code: "api_error", Status: 503}
		}
		result, err := e.Sync(t.Context(), Options{})
		if Code(err) != "api_error" || result.Status != "failed" || result.HTTPStatus != 503 || result.SQLiteCode != 0 {
			t.Fatal("failed run omitted its numeric HTTP status")
		}
	})
	t.Run("sqlite", func(t *testing.T) {
		e, _ := fixture(t)
		if _, err := e.Store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_publication BEFORE DELETE ON records BEGIN SELECT RAISE(FAIL,'private synthetic SQL detail'); END`); err != nil {
			t.Fatal(err)
		}
		m := e.Metrics[0]
		w := Window{Since: "2026-10-01", Until: "2026-10-02"}
		if _, err := e.Store.db.ExecContext(t.Context(), `INSERT INTO records
 (stream,record_key,data_type,kind,anchor,start_time,end_time,observation_date,source_json,source_family,original_json,has_value,imported_at)
 VALUES(?,'synthetic','steps','interval','2026-10-01','','','','{}','synthetic','{}',1,'')`, m.Stream()); err != nil {
			t.Fatal(err)
		}
		result, err := e.Backfill(t.Context(), Options{Since: w.Since, Until: w.Until})
		if Code(err) != "storage_error" || result.Status != "failed" || result.SQLiteCode != 1811 || result.HTTPStatus != 0 {
			t.Fatal("failed run omitted its numeric SQLite constraint code")
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil || strings.Contains(string(encoded), "private synthetic") {
			t.Fatal("failed run exposed an SQL message")
		}
	})
}

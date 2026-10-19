package web_test

import (
	"net/http/httptest"
	"testing"

	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/testfixture"
)

func TestOperationalHealthTracksPersistenceWithoutIdentityNetwork(t *testing.T) {
	f := testfixture.New(t)
	handler := newHandler(t, f, shopping.New(f.Store))
	status := func(path string) int {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		return response.Code
	}
	f.SetUnavailable(true)
	if status("/health/ready") != 200 || status("/health/live") != 200 {
		t.Fatal("configured identity's transient outage changed operational health")
	}
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if status("/health/ready") != 503 || status("/health/live") != 200 {
		t.Fatal("health failed to distinguish closed persistence from process liveness")
	}
}

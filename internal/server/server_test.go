package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/server"
	"github.com/badilhq/badil/internal/server/mocks"
)

func TestHealthz(t *testing.T) {
	m := mocks.NewMockWebhookValidator(t)
	srv := server.NewServer(m, nil, "test-deploy-token", 0)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestDeployAPI_RoutesAbsentWithoutToken(t *testing.T) {
	srv := server.NewServer(nil, nil, "", 0)
	for _, path := range []string{"/v1/deploy", "/v1/sync"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer ")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

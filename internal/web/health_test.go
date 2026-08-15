package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Levipanic/jamcontests/internal/testdb"
)

func TestHealthReportsSchemaAndStaysCookieFree(t *testing.T) {
	pool := testdb.Open(t)
	cfg := publicTestConfig(t)
	cfg.MigrationsDir = "../../migrations"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := New(cfg, pool, logger)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("health body = %s", recorder.Body.String())
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("health endpoint set cookies: %v", cookies)
	}

	if _, err := pool.Exec(context.Background(), `DELETE FROM schema_migrations WHERE name = '001_initial.sql'`); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale schema health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

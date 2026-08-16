package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestJamScheduleValidationAdmin(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "scheduleadmin", "admin")
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionCookie := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 24)

	get := httptest.NewRequest(http.MethodGet, "/admin/jams/new", nil)
	get.AddCookie(sessionCookie)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("jam form status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	csrfCookie := responseCookie(t, recorder.Result(), csrfCookieName)

	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(72 * time.Hour).In(moscow)
	formatDate := func(offset time.Duration) string {
		return future.Add(offset).Format("2006-01-02T15:04")
	}

	create := func(values url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/admin/jams/new", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	unordered := url.Values{
		"title":                {"Неправильное расписание"},
		"description":          {"Тест"},
		"rules":                {"Правила"},
		"max_team_size":        {"4"},
		"submission_starts_at": {formatDate(48 * time.Hour)},
		"evaluation_starts_at": {formatDate(24 * time.Hour)},
		"voting_starts_at":     {formatDate(72 * time.Hour)},
		"finishes_at":          {formatDate(96 * time.Hour)},
		"reason":               {"Проверка порядка"},
		"csrf_token":           {csrfCookie.Value},
	}
	recorder = create(unordered)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "строго по порядку") {
		t.Fatalf("unordered schedule accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	pastStart := url.Values{
		"title":                {"Расписание в прошлом"},
		"description":          {"Тест"},
		"rules":                {"Правила"},
		"max_team_size":        {"4"},
		"submission_starts_at": {time.Now().Add(-2 * time.Hour).In(moscow).Format("2006-01-02T15:04")},
		"evaluation_starts_at": {formatDate(24 * time.Hour)},
		"voting_starts_at":     {formatDate(72 * time.Hour)},
		"finishes_at":          {formatDate(96 * time.Hour)},
		"reason":               {"Проверка будущего"},
		"csrf_token":           {csrfCookie.Value},
	}
	recorder = create(pastStart)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "в будущем") {
		t.Fatalf("past schedule accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	valid := url.Values{
		"title":                {"Правильное расписание"},
		"description":          {"Тест"},
		"rules":                {"Правила"},
		"max_team_size":        {"4"},
		"submission_starts_at": {formatDate(24 * time.Hour)},
		"evaluation_starts_at": {formatDate(48 * time.Hour)},
		"voting_starts_at":     {formatDate(72 * time.Hour)},
		"finishes_at":          {formatDate(96 * time.Hour)},
		"reason":               {"Создание"},
		"csrf_token":           {csrfCookie.Value},
	}
	recorder = create(valid)
	if recorder.Code != http.StatusSeeOther || !strings.HasPrefix(recorder.Header().Get("Location"), "/admin/jams/") {
		t.Fatalf("valid schedule rejected: status=%d location=%s body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}

	var jamID int64
	if err = pool.QueryRow(ctx, "SELECT id FROM jams ORDER BY id DESC LIMIT 1").Scan(&jamID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE jams SET voting_starts_at=finishes_at
		WHERE id=$1`, jamID); err == nil {
		t.Fatal("database accepted a schedule with voting == finish")
	}
	var constraintErr *pgconn.PgError
	if !errors.As(err, &constraintErr) || constraintErr.Code != "23514" {
		t.Fatalf("unexpected constraint error: %v", err)
	}
}

func TestJamScheduleOrderDirectViolation(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
		INSERT INTO jams (title, description, rules, submission_starts_at, evaluation_starts_at,
		                  voting_starts_at, finishes_at, max_team_size)
		VALUES ('Плохой порядок', '', '', '2030-01-02T10:00:00Z', '2030-01-01T10:00:00Z',
		        '2030-01-03T10:00:00Z', '2030-01-04T10:00:00Z', 4)`)
	if err == nil {
		t.Fatal("insert with unordered schedule succeeded despite CHECK constraint")
	}
	var constraintErr *pgconn.PgError
	if !errors.As(err, &constraintErr) || constraintErr.Code != "23514" {
		t.Fatalf("unexpected constraint error: %v", err)
	}
}

package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func insertDraftJamFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string) int64 {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var jamID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO jams (title, description, rules, submission_starts_at, evaluation_starts_at,
		                  voting_starts_at, finishes_at, max_team_size, visibility)
		VALUES ($1, '', '', clock_timestamp()+interval '10 days', clock_timestamp()+interval '12 days',
		        clock_timestamp()+interval '14 days', clock_timestamp()+interval '16 days', 4, 'draft')
		RETURNING id`, title).Scan(&jamID); err != nil {
		t.Fatal(err)
	}
	var questionnaireID int64
	if err = tx.QueryRow(ctx, `INSERT INTO questionnaires (jam_id) VALUES ($1) RETURNING id`, jamID).Scan(&questionnaireID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, required, text_limit, position) VALUES ($1, 'short_text', 'Вопрос черновика', true, 100, 0)`, questionnaireID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO jam_themes (jam_id, phrase) VALUES ($1, 'Тема черновика')`, jamID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return jamID
}

func performJamDeleteRequest(t *testing.T, router http.Handler, session, csrf *http.Cookie, jamID int64, confirm string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"csrf_token": {csrf.Value}}
	if confirm != "" {
		form.Set("confirm_destroy", confirm)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/jams/"+formatID(jamID)+"/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func deleteJamAdminFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, *http.Cookie, int64, http.Handler) {
	t.Helper()
	jamID := insertDraftJamFixture(t, ctx, pool, "Черновик к удалению")
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "deleteadmin", "admin")
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 25)
	return jamID, session, adminID, router
}

func TestDeleteDraftJamAdmin(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jamID, session, _, router := deleteJamAdminFixture(t, ctx, pool)
	page := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/delete", nil)
	request.AddCookie(session)
	router.ServeHTTP(page, request)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Удалить джем") || !strings.Contains(page.Body.String(), "Черновик к удалению") {
		t.Fatalf("delete confirmation page status=%d body=%s", page.Code, page.Body.String())
	}
	csrf := responseCookie(t, page.Result(), csrfCookieName)

	recorder := performJamDeleteRequest(t, router, session, csrf, jamID, "yes")
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "ok=") {
		t.Fatalf("delete status=%d location=%s", recorder.Code, recorder.Header().Get("Location"))
	}
	var jamCount, questionCount, themeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jams WHERE id=$1`, jamID).Scan(&jamCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM questionnaire_questions qq JOIN questionnaires q ON q.id=qq.questionnaire_id WHERE q.jam_id=$1`, jamID).Scan(&questionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jam_themes WHERE jam_id=$1`, jamID).Scan(&themeCount); err != nil {
		t.Fatal(err)
	}
	if jamCount != 0 || questionCount != 0 || themeCount != 0 {
		t.Fatalf("jam subtree survived deletion: jams=%d questions=%d themes=%d", jamCount, questionCount, themeCount)
	}
}

func TestDeleteDraftJamRequiresConfirmation(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jamID, session, _, router := deleteJamAdminFixture(t, ctx, pool)
	page := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/delete", nil)
	request.AddCookie(session)
	router.ServeHTTP(page, request)
	csrf := responseCookie(t, page.Result(), csrfCookieName)

	noConfirm := performJamDeleteRequest(t, router, session, csrf, jamID, "")
	if noConfirm.Code != http.StatusConflict || !strings.Contains(noConfirm.Body.String(), "Подтвердите уничтожение") {
		t.Fatalf("delete without confirmation status=%d body=%s", noConfirm.Code, noConfirm.Body.String())
	}
	var jamCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jams WHERE id=$1`, jamID).Scan(&jamCount); err != nil {
		t.Fatal(err)
	}
	if jamCount != 1 {
		t.Fatalf("jam deleted despite failed guards, jams=%d", jamCount)
	}
}

func TestDeletePublishedJamRejected(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jamID := insertDraftJamFixture(t, ctx, pool, "Публиковавшийся джем")
	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published' WHERE id=$1`, jamID); err != nil {
		t.Fatal(err)
	}
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "publishadmin", "admin")
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 27)

	page := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/delete", nil)
	request.AddCookie(session)
	router.ServeHTTP(page, request)
	if page.Code != http.StatusSeeOther || !strings.Contains(page.Header().Get("Location"), "error=") {
		t.Fatalf("published jam delete page status=%d location=%s", page.Code, page.Header().Get("Location"))
	}

	csrfPage := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/edit", nil)
	request.AddCookie(session)
	router.ServeHTTP(csrfPage, request)
	csrf := responseCookie(t, csrfPage.Result(), csrfCookieName)
	recorder := performJamDeleteRequest(t, router, session, csrf, jamID, "yes")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("published jam delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var jamCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jams WHERE id=$1`, jamID).Scan(&jamCount); err != nil {
		t.Fatal(err)
	}
	if jamCount != 1 {
		t.Fatalf("published jam deleted, jams=%d", jamCount)
	}
}

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
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

func TestQuestionnaireCSVCell(t *testing.T) {
	for _, value := range []string{"=1+1", "+cmd", "-2+3", "@SUM", "\t=SUM(1)", "  =SUM(1)"} {
		if got := questionnaireCSVCell(value); !strings.HasPrefix(got, "'") {
			t.Errorf("unsafe cell %q became %q", value, got)
		}
	}
	for _, value := range []string{"обычный текст", "42", "текст, с запятой", `"кавычки"`} {
		if got := questionnaireCSVCell(value); got != value {
			t.Errorf("safe cell %q changed to %q", value, got)
		}
	}
}

func TestQuestionnaireResetAndAdminReports(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "reportadmin", "admin")
	memberID := insertQuestionnaireReportUser(t, ctx, pool, "formulamember", "user")
	jamID, questionnaireID, questionID, responseID := insertQuestionnaireReportFixture(t, ctx, pool, memberID)
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionCookie := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 9)

	get := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire", nil)
	get.AddCookie(sessionCookie)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("questionnaire admin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	csrfCookie := responseCookie(t, recorder.Result(), csrfCookieName)
	form := url.Values{"confirm": {"СБРОСИТЬ ОТВЕТЫ"}, "csrf_token": {csrfCookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "/admin/jams/"+formatID(jamID)+"/questionnaire/reset", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("reset status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var revision, currentResponses, historyEvents int
	if err := pool.QueryRow(ctx, `SELECT current_revision FROM questionnaires WHERE id=$1`, questionnaireID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM questionnaire_responses WHERE questionnaire_id=$1 AND revision=2`, questionnaireID).Scan(&currentResponses); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM questionnaire_response_history WHERE response_id=$1 AND event='admin_reset'`, responseID).Scan(&historyEvents); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || currentResponses != 0 || historyEvents != 1 {
		t.Fatalf("unexpected reset state revision=%d responses=%d history=%d", revision, currentResponses, historyEvents)
	}
	var clonedQuestionID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM questionnaire_questions WHERE questionnaire_id=$1 AND revision=2`, questionnaireID).Scan(&clonedQuestionID); err != nil {
		t.Fatal(err)
	}
	if clonedQuestionID == questionID {
		t.Fatal("reset reused historical question ID")
	}
	app := &App{pool: pool}
	questions, status, err := app.questionnaireLoadOwnAnswers(ctx, questionnaireID, memberID)
	if err != nil || len(questions) != 1 || status != "draft" || questions[0].ID != clonedQuestionID {
		t.Fatalf("load current questionnaire after reset: questions=%+v status=%q err=%v", questions, status, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE questionnaire_text_answers SET value='changed' WHERE response_id=$1`, responseID); err == nil {
		t.Fatal("historical answer remained mutable after reset")
	}
	if _, err := pool.Exec(ctx, `UPDATE questionnaire_questions SET revision=2, position=99, prompt='changed' WHERE id=$1`, questionID); err == nil {
		t.Fatal("historical question could be moved into current revision")
	}
	if _, err := pool.Exec(ctx, `UPDATE questionnaire_responses SET revision=2 WHERE id=$1`, responseID); err == nil {
		t.Fatal("historical response could be moved into current revision")
	}
	if _, err := pool.Exec(ctx, `UPDATE questionnaires SET current_revision=1 WHERE id=$1`, questionnaireID); err == nil {
		t.Fatal("questionnaire revision could be rolled back")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE questionnaire_options CASCADE`); err == nil {
		t.Fatal("historical questionnaire data could be truncated")
	}
	if _, err := pool.Exec(ctx, `UPDATE questionnaire_response_history SET snapshot='{}'::jsonb WHERE response_id=$1`, responseID); err == nil {
		t.Fatal("questionnaire history remained mutable")
	}
	var oldResponseEligible bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_members member
			JOIN questionnaires questionnaire ON questionnaire.jam_id=member.jam_id
			JOIN questionnaire_responses response ON response.questionnaire_id=questionnaire.id
			  AND response.revision=questionnaire.current_revision
			  AND response.user_id=member.user_id AND response.status='completed'
			WHERE member.user_id=$1
		)`, memberID).Scan(&oldResponseEligible); err != nil {
		t.Fatal(err)
	}
	if oldResponseEligible {
		t.Fatal("historical completed response still grants eligibility")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO questionnaire_responses (questionnaire_id, revision, team_id_at_start, user_id)
		SELECT id, current_revision, (SELECT team_id FROM team_members WHERE user_id=$2 AND jam_id=$3), $2
		FROM questionnaires WHERE id=$1`, questionnaireID, memberID, jamID); err != nil {
		t.Fatalf("create response in new revision: %v", err)
	}

	reportRequest := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/reports", nil)
	reportRequest.AddCookie(sessionCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, reportRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "formulamember") {
		t.Fatalf("report status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	detailRequest := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/reports/responses/"+formatID(responseID), nil)
	detailRequest.AddCookie(sessionCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, detailRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "=SUM(1,1)") {
		t.Fatalf("response report status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	csvRequest := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/reports.csv", nil)
	csvRequest.AddCookie(sessionCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, csvRequest)
	if recorder.Code != http.StatusOK || !bytes.HasPrefix(recorder.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("CSV status=%d", recorder.Code)
	}
	reader := csv.NewReader(bytes.NewReader(recorder.Body.Bytes()[3:]))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	foundSafeFormula := false
	for _, record := range records {
		for _, cell := range record {
			if cell == "'=SUM(1,1)" {
				foundSafeFormula = true
			}
		}
	}
	if !foundSafeFormula {
		t.Fatal("CSV did not neutralize formula answer")
	}
	guest := httptest.NewRecorder()
	router.ServeHTTP(guest, httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/reports", nil))
	if guest.Code != http.StatusNotFound || strings.Contains(guest.Body.String(), "=SUM") {
		t.Fatalf("guest report status=%d body=%s", guest.Code, guest.Body.String())
	}
	userSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", memberID, 10)
	userRequest := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/reports", nil)
	userRequest.AddCookie(userSession)
	userRecorder := httptest.NewRecorder()
	router.ServeHTTP(userRecorder, userRequest)
	if userRecorder.Code != http.StatusForbidden || strings.Contains(userRecorder.Body.String(), "=SUM") {
		t.Fatalf("user report status=%d body=%s", userRecorder.Code, userRecorder.Body.String())
	}
}

func insertQuestionnaireReportUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username, role string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash, role) VALUES ($1, 'test', $2) RETURNING id`, username, role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertQuestionnaireReportFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, memberID int64) (int64, int64, int64, int64) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var jamID, questionnaireID, teamID, questionID, responseID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, max_team_size)
		VALUES ('Questionnaire report', 'draft', now()+interval '1 day', now()+interval '2 days', now()+interval '3 days', now()+interval '4 days', 5) RETURNING id`).Scan(&jamID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO questionnaires (jam_id) VALUES ($1) RETURNING id`, jamID).Scan(&questionnaireID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, required, text_limit, position) VALUES ($1, 'short_text', 'Опасный ответ?', true, 100, 0) RETURNING id`, questionnaireID).Scan(&questionID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO teams (jam_id, name, captain_user_id) VALUES ($1, 'Report Team', $2) RETURNING id`, jamID, memberID).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, memberID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO questionnaire_responses (questionnaire_id, revision, team_id_at_start, user_id, status, completed_at) VALUES ($1, 1, $2, $3, 'completed', now()) RETURNING id`, questionnaireID, teamID, memberID).Scan(&responseID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO questionnaire_text_answers (response_id, question_id, value) VALUES ($1, $2, '=SUM(1,1)')`, responseID, questionID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return jamID, questionnaireID, questionID, responseID
}

func insertQuestionnaireReportSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, userID int64, fill byte) *http.Cookie {
	t.Helper()
	raw := bytes.Repeat([]byte{fill}, 32)
	hash := sha256.Sum256(raw)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now()+interval '1 hour')`, hash[:], userID); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: name, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/"}
}

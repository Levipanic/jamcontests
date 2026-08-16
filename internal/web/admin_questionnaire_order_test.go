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

func TestQuestionnaireOrderAdmin(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "orderadmin", "admin")
	jamID, questionnaireID, questions := insertQuestionnaireOrderFixture(t, ctx, pool)
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionCookie := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 23)

	get := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire", nil)
	get.AddCookie(sessionCookie)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("questionnaire admin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	csrfCookie := responseCookie(t, recorder.Result(), csrfCookieName)

	reversed := []int64{questions[2], questions[0], questions[1]}
	form := url.Values{
		"order":      {formatID(reversed[0]) + "," + formatID(reversed[1]) + "," + formatID(reversed[2])},
		"reason":     {"Новый порядок обсуждён с кураторами"},
		"csrf_token": {csrfCookie.Value},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/jams/"+formatID(jamID)+"/questionnaire/order", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "ok=") {
		t.Fatalf("questionnaire order status=%d location=%s", recorder.Code, recorder.Header().Get("Location"))
	}

	rows, err := pool.Query(ctx, `
		SELECT question.id, question.position FROM questionnaire_questions question
		JOIN questionnaires questionnaire ON questionnaire.id=question.questionnaire_id
		WHERE question.questionnaire_id=$1 AND question.revision=questionnaire.current_revision
		ORDER BY question.position, question.id`, questionnaireID)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[int64]int{}
	for rows.Next() {
		var id int64
		var position int
		if err = rows.Scan(&id, &position); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		positions[id] = position
	}
	rows.Close()
	for index, id := range reversed {
		if positions[id] != index {
			t.Fatalf("question %d expected position %d got %d (positions=%v)", id, index, positions[id], positions)
		}
	}
	var auditCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_log WHERE action='questionnaire.question_order' AND entity_id=$1`, questionnaireID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	var beforeData, afterData string
	if err = pool.QueryRow(ctx, `
		SELECT COALESCE(l.before_data::text, ''), COALESCE(l.after_data::text, '')
		FROM admin_audit_log l WHERE l.action='questionnaire.question_order' AND l.entity_id=$1
		ORDER BY l.id DESC LIMIT 1`, questionnaireID).Scan(&beforeData, &afterData); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || !strings.Contains(beforeData, `"position": 0`) || !strings.Contains(afterData, `"position": 2`) {
		t.Fatalf("questionnaire order audit missing: count=%d before=%s after=%s", auditCount, beforeData, afterData)
	}

	badSet := url.Values{"order": {formatID(reversed[0]) + "," + formatID(reversed[1])}, "reason": {"потерян один вопрос"}, "csrf_token": {csrfCookie.Value}}
	badRequest := httptest.NewRequest(http.MethodPost, "/admin/jams/"+formatID(jamID)+"/questionnaire/order", strings.NewReader(badSet.Encode()))
	badRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRequest.AddCookie(sessionCookie)
	badRequest.AddCookie(csrfCookie)
	badRecorder := httptest.NewRecorder()
	router.ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusConflict {
		t.Fatalf("mismatched order set status=%d body=%s", badRecorder.Code, badRecorder.Body.String())
	}

	guest := httptest.NewRecorder()
	router.ServeHTTP(guest, httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire/order", nil))
	if guest.Code != http.StatusMethodNotAllowed && guest.Code != http.StatusNotFound {
		t.Fatalf("guest order status=%d", guest.Code)
	}
}

func TestQuestionnaireOrderLocked(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "lockadmin", "admin")
	memberID := insertQuestionnaireReportUser(t, ctx, pool, "lockmember", "user")
	jamID, _, questions := insertQuestionnaireOrderFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published' WHERE id=$1`, jamID); err != nil {
		t.Fatal(err)
	}
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionCookie := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 24)
	get := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(jamID)+"/questionnaire", nil)
	get.AddCookie(sessionCookie)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("locked questionnaire page status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	csrfCookie := responseCookie(t, recorder.Result(), csrfCookieName)
	form := url.Values{"order": {formatID(questions[2]) + "," + formatID(questions[0]) + "," + formatID(questions[1])}, "reason": {"попытка перестановки"}, "csrf_token": {csrfCookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "/admin/jams/"+formatID(jamID)+"/questionnaire/order", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("locked questionnaire order status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var positionsChanged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM questionnaire_questions WHERE position<>0 AND position<>1 AND position<>2`).Scan(&positionsChanged); err != nil {
		t.Fatal(err)
	}
	if positionsChanged != 0 {
		t.Fatalf("locked questionnaire positions mutated: %d", positionsChanged)
	}
	if memberID == 0 {
		t.Fatal("member fixture not used")
	}
}

func insertQuestionnaireOrderFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, int64, []int64) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var jamID, questionnaireID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, max_team_size)
		VALUES ('Order fixture', 'draft', now()+interval '1 day', now()+interval '2 days', now()+interval '3 days', now()+interval '4 days', 5) RETURNING id`).Scan(&jamID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO questionnaires (jam_id) VALUES ($1) RETURNING id`, jamID).Scan(&questionnaireID); err != nil {
		t.Fatal(err)
	}
	prompts := []string{"Первый", "Второй", "Третий"}
	questionIDs := make([]int64, 3)
	for index, prompt := range prompts {
		if err = tx.QueryRow(ctx, `INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, required, text_limit, position) VALUES ($1, 'short_text', $2, false, 500, $3) RETURNING id`, questionnaireID, prompt, index).Scan(&questionIDs[index]); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return jamID, questionnaireID, questionIDs
}

func TestAuditDiff(t *testing.T) {
	diff := auditDiff(`{"title":"Старое название","status":"draft"}`, `{"title":"Новое название","status":"draft"}`)
	if diff == "" {
		t.Fatal("auditDiff returned empty for valid objects")
	}
	if !strings.Contains(string(diff), "audit-diff-changed") || !strings.Contains(string(diff), "Старое название") || !strings.Contains(string(diff), "Новое название") {
		t.Fatalf("auditDiff lost changed value: %q", diff)
	}
	if strings.Contains(string(diff), "<script>") {
		t.Fatal("auditDiff did not escape untrusted text")
	}
	escaped := auditDiff(`{"reason":"<script>alert(1)</script>"}`, `{"reason":"ok"}`)
	if strings.Contains(string(escaped), "<script>") {
		t.Fatalf("auditDiff failed to escape: %q", escaped)
	}
	if got := auditDiff("не json", `{}`); got != "" {
		t.Fatalf("auditDiff accepted malformed JSON: %q", got)
	}
	if got := auditDiff(`[1,2]`, `[2,1]`); got != "" {
		t.Fatalf("auditDiff rendered an array payload: %q", got)
	}
	unchanged := auditDiff(`{"a":1}`, `{"a":1}`)
	if strings.Contains(string(unchanged), "audit-diff-changed") {
		t.Fatalf("auditDiff flagged unchanged values: %q", unchanged)
	}
}

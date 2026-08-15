package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEndToEndJamJourneyRealUsers drives one complete jam lifecycle through the
// public HTTP surface with real registered users: registration and login, team
// creation, invitation join, eligibility questionnaire, theme selection,
// product card and final submission, evaluation bumps, voting, and finished
// results with nomination authorship. Stage transitions are performed through
// SQL overrides (the admin UI itself is covered by the admin tests).
func TestEndToEndJamJourneyRealUsers(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	insertJourneyAdmin(t, ctx, pool)
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))

	jamID, jamPublic := insertJourneyJam(t, ctx, pool)
	var themeID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM jam_themes WHERE jam_id=$1 ORDER BY id LIMIT 1`, jamID).Scan(&themeID); err != nil {
		t.Fatal(err)
	}
	questions := journeyQuestionIDs(t, ctx, pool, jamID)
	questionShort, questionSingle, questionMultiple := questions[0], questions[1], questions[2]
	var singleOption, multipleOption int64
	if err := pool.QueryRow(ctx, `SELECT id FROM questionnaire_options WHERE question_id=$1 ORDER BY id LIMIT 1`, questionSingle).Scan(&singleOption); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM questionnaire_options WHERE question_id=$1 ORDER BY id LIMIT 1`, questionMultiple).Scan(&multipleOption); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published' WHERE id=$1`, jamID); err != nil {
		t.Fatal(err)
	}

	captain := journeyUser{Jar: newJourneyJar()}
	member := journeyUser{Jar: newJourneyJar()}
	outsider := journeyUser{Jar: newJourneyJar()}
	for _, account := range []struct {
		user     *journeyUser
		username string
	}{
		{&captain, "journeycaptain"},
		{&member, "journeymember"},
		{&outsider, "journeyoutsider"},
	} {
		journeyRegister(t, router, account.user, account.username, "journey-pass-123")
	}

	// Upcoming: a guest sees the public jam page without prepared themes.
	jamPage := journeyGet(t, router, nil, "/jams/"+jamPublic, http.StatusOK)
	if strings.Contains(jamPage.Body.String(), journeyThemeOne) || strings.Contains(jamPage.Body.String(), journeyThemeTwo) {
		t.Fatal("upcoming jam page disclosed prepared themes")
	}

	// Captain creates the team and completes the eligibility questionnaire.
	journeyGet(t, router, &captain, "/jams/"+jamPublic+"/teams/new", http.StatusOK)
	createResponse := journeyPostMultipart(t, router, &captain, "/jams/"+jamPublic+"/teams/new", url.Values{
		"name": {"Journey Team"}, "description": {"Команда пути"}, "csrf_token": {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	teamPublic := journeyTeamPublicFromLocation(t, createResponse.Header().Get("Location"))
	journeyCompleteQuestionnaire(t, router, &captain, routePrefix(jamPublic), questionShort, questionSingle, questionMultiple, singleOption, multipleOption)

	teamPage := journeyGet(t, router, &captain, "/teams/"+teamPublic, http.StatusOK)
	if strings.Contains(teamPage.Body.String(), "/theme") {
		t.Fatal("upcoming team page offered theme selection")
	}
	if !strings.Contains(teamPage.Body.String(), "завершён") {
		t.Fatal("team page does not show captain questionnaire completion status")
	}

	// Invitation issued once, then joined by the member.
	issued := journeyPostForm(t, router, &captain, "/teams/"+teamPublic+"/invite", url.Values{"csrf_token": {captain.Jar.csrfCookie()}}, http.StatusOK)
	token := journeyInviteToken(t, issued.Body.String())
	journeyGet(t, router, &member, "/invites/"+token, http.StatusOK)
	joinResponse := journeyPostForm(t, router, &member, "/invites/"+token, url.Values{"csrf_token": {member.Jar.csrfCookie()}}, http.StatusSeeOther)
	if !strings.HasPrefix(joinResponse.Header().Get("Location"), "/teams/"+teamPublic) {
		t.Fatalf("member join redirected to %q", joinResponse.Header().Get("Location"))
	}
	journeyCompleteQuestionnaire(t, router, &member, routePrefix(jamPublic), questionShort, questionSingle, questionMultiple, singleOption, multipleOption)
	memberTeamPage := journeyGet(t, router, &member, "/teams/"+teamPublic, http.StatusOK)
	if !strings.Contains(memberTeamPage.Body.String(), "journeycaptain") || !strings.Contains(memberTeamPage.Body.String(), "journeymember") {
		t.Fatal("team page does not list both members")
	}
	if strings.Contains(memberTeamPage.Body.String(), journeyQuestionAnswer) {
		t.Fatal("team page disclosed another member's questionnaire answers")
	}

	// Submission: themes revealed, captain picks one, a member cannot, the
	// product is saved and finalized, and stays hidden from guests.
	setJamOverride(t, ctx, pool, jamID, "submission")
	submissionJamPage := journeyGet(t, router, nil, "/jams/"+jamPublic, http.StatusOK)
	if !strings.Contains(submissionJamPage.Body.String(), journeyThemeOne) && !strings.Contains(submissionJamPage.Body.String(), journeyThemeTwo) {
		t.Fatal("submission jam page does not reveal themes")
	}
	journeyPostForm(t, router, &captain, "/teams/"+teamPublic+"/theme", url.Values{
		"theme_id": {formatID(themeID)}, "csrf_token": {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	memberThemeResponse := journeyPostForm(t, router, &member, "/teams/"+teamPublic+"/theme", url.Values{
		"theme_id": {formatID(themeID)}, "csrf_token": {member.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	if strings.Contains(memberThemeResponse.Header().Get("Location"), "selected") {
		t.Fatal("non-captain member changed the theme selection")
	}
	journeyGet(t, router, &captain, "/jams/"+jamPublic+"/product", http.StatusOK)
	journeyPostForm(t, router, &captain, "/jams/"+jamPublic+"/product/save", url.Values{
		"title":            {journeyProductTitle},
		"result_url":       {"https://example.test/journey-result"},
		"description":      {"Описание результата"},
		"notes":            {"ПРИВАТНАЯ ЗАМЕТКА"},
		"nomination_title": {journeyNomination},
		"csrf_token":       {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	journeyPostForm(t, router, &captain, "/jams/"+jamPublic+"/product/finalize", url.Values{"csrf_token": {captain.Jar.csrfCookie()}}, http.StatusSeeOther)
	productPublic := journeyProductPublic(t, ctx, pool, jamID)
	guestProducts := journeyGet(t, router, nil, "/jams/"+jamPublic+"/products", http.StatusNotFound)
	if strings.Contains(guestProducts.Body.String(), journeyProductTitle) {
		t.Fatal("submission products list leaked product to a guest")
	}
	guestTeamPage := journeyGet(t, router, nil, "/teams/"+teamPublic, http.StatusOK)
	if strings.Contains(guestTeamPage.Body.String(), "<dt>Тема</dt>") && strings.Contains(guestTeamPage.Body.String(), journeyThemeOne) {
		t.Fatal("submission team page disclosed the selection to a guest")
	}

	// Evaluation: products open, an outsider bumps once and the cooldown
	// rejects the immediate repeat.
	setJamOverride(t, ctx, pool, jamID, "evaluation")
	evaluationList := journeyGet(t, router, nil, "/jams/"+jamPublic+"/products", http.StatusOK)
	if !strings.Contains(evaluationList.Body.String(), journeyProductTitle) {
		t.Fatal("evaluation products list omits the product")
	}
	productDetail := journeyGet(t, router, nil, "/products/"+productPublic, http.StatusOK)
	if strings.Contains(productDetail.Body.String(), "ПРИВАТНАЯ ЗАМЕТКА") {
		t.Fatal("product detail disclosed private notes")
	}
	firstBump := journeyPostJSON(t, router, &outsider, "/api/products/"+productPublic+"/bumps", nil, http.StatusOK)
	if !strings.Contains(firstBump.Body.String(), `"count":1`) || !strings.Contains(firstBump.Body.String(), `"bumped":true`) {
		t.Fatalf("first bump body=%s", firstBump.Body.String())
	}
	secondBump := journeyPostJSON(t, router, &outsider, "/api/products/"+productPublic+"/bumps", nil, http.StatusTooManyRequests)
	if !strings.Contains(secondBump.Body.String(), `"bumped":false`) {
		t.Fatalf("cooldown bump body=%s", secondBump.Body.String())
	}

	// Voting: curator nomination added by admin, team nomination appears
	// without authorship, counts are public, self and guest votes are rejected.
	insertJourneyCuratorNomination(t, ctx, pool, jamID)
	setJamOverride(t, ctx, pool, jamID, "voting")
	votingPage := journeyGet(t, router, nil, "/jams/"+jamPublic+"/nominations", http.StatusOK)
	for _, required := range []string{journeyNomination, "Приз зрительских симпатий"} {
		if !strings.Contains(votingPage.Body.String(), required) {
			t.Fatalf("voting page lacks %q", required)
		}
	}
	if strings.Contains(votingPage.Body.String(), "Автор номинации:") {
		t.Fatal("voting page disclosed team nomination authorship")
	}
	nominationPublic := journeyNominationPublic(t, ctx, pool, jamID)
	counts := journeyGet(t, router, nil, "/api/jams/"+jamPublic+"/vote-counts", http.StatusOK)
	if !strings.Contains(counts.Body.String(), productPublic) {
		t.Fatalf("vote counts body=%s", counts.Body.String())
	}
	outsiderVote := journeyPostJSON(t, router, &outsider, "/api/jams/"+jamPublic+"/nominations/"+nominationPublic+"/vote",
		voteRequest{ProductID: productPublic}, http.StatusOK)
	if !strings.Contains(outsiderVote.Body.String(), productPublic) {
		t.Fatalf("outsider vote body=%s", outsiderVote.Body.String())
	}
	journeyPostJSON(t, router, &captain, "/api/jams/"+jamPublic+"/nominations/"+nominationPublic+"/vote",
		voteRequest{ProductID: productPublic}, http.StatusUnprocessableEntity)
	guest := journeyUser{Jar: newJourneyJar()}
	journeyGet(t, router, &guest, "/api/jams/"+jamPublic+"/vote-counts", http.StatusOK)
	journeyPostJSON(t, router, &guest, "/api/jams/"+jamPublic+"/nominations/"+nominationPublic+"/vote",
		voteRequest{ProductID: productPublic}, http.StatusUnauthorized)

	// Finished: results and authorship are public, mutations are closed.
	setJamOverride(t, ctx, pool, jamID, "finished")
	finishedPage := journeyGet(t, router, nil, "/jams/"+jamPublic+"/nominations", http.StatusOK)
	for _, required := range []string{"Победитель номинации", "Автор номинации: Journey Team", "Приз зрительских симпатий"} {
		if !strings.Contains(finishedPage.Body.String(), required) {
			t.Fatalf("finished page lacks %q", required)
		}
	}
	journeyPostJSON(t, router, &outsider, "/api/jams/"+jamPublic+"/nominations/"+nominationPublic+"/vote",
		voteRequest{ProductID: productPublic}, http.StatusConflict)
	journeyPostJSON(t, router, &outsider, "/api/products/"+productPublic+"/bumps", nil, http.StatusConflict)
	archivePage := journeyGet(t, router, nil, "/archive", http.StatusOK)
	if !strings.Contains(archivePage.Body.String(), "/jams/"+jamPublic) {
		t.Fatal("archive does not list the finished jam")
	}
}

const (
	journeyThemeOne       = "Тема-путник"
	journeyThemeTwo       = "Тема-маяк"
	journeyNomination     = "Наша номинация"
	journeyProductTitle   = "Путевой результат"
	journeyQuestionAnswer = "Мой ответ"
)

type journeyUser struct {
	Jar *journeyJar
}

type journeyJar struct {
	cookies map[string]*http.Cookie
}

func newJourneyJar() *journeyJar {
	return &journeyJar{cookies: map[string]*http.Cookie{}}
}

func (jar *journeyJar) capture(recorder *httptest.ResponseRecorder) {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Value != "" {
			jar.cookies[cookie.Name] = cookie
		}
	}
}

func (jar *journeyJar) apply(request *http.Request) {
	for _, cookie := range jar.cookies {
		request.AddCookie(cookie)
	}
}

func (jar *journeyJar) csrfCookie() string {
	if cookie := jar.cookies[csrfCookieName]; cookie != nil {
		return cookie.Value
	}
	return ""
}

func journeyGet(t *testing.T, router http.Handler, user *journeyUser, path string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if user != nil {
		user.Jar.apply(request)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if user != nil {
		user.Jar.capture(recorder)
	}
	if recorder.Code != want {
		t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

func journeyPostForm(t *testing.T, router http.Handler, user *journeyUser, path string, form url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		user.Jar.apply(request)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if user != nil {
		user.Jar.capture(recorder)
	}
	if recorder.Code != want {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

// journeyPostMultipart posts url-encoded values as multipart/form-data without
// any file part, matching the team forms that carry an optional avatar.
func journeyPostMultipart(t *testing.T, router http.Handler, user *journeyUser, path string, form url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, values := range form {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if user != nil {
		user.Jar.apply(request)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if user != nil {
		user.Jar.capture(recorder)
	}
	if recorder.Code != want {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

func journeyPostJSON(t *testing.T, router http.Handler, user *journeyUser, path string, payload any, want int) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if user != nil {
		user.Jar.apply(request)
		request.Header.Set("X-CSRF-Token", user.Jar.csrfCookie())
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if user != nil {
		user.Jar.capture(recorder)
	}
	if recorder.Code != want {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

func journeyRegister(t *testing.T, router http.Handler, user *journeyUser, username, password string) {
	t.Helper()
	journeyGet(t, router, user, "/register", http.StatusOK)
	journeyPostForm(t, router, user, "/register", url.Values{
		"username": {username}, "password": {password}, "email": {""}, "csrf_token": {user.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	journeyGet(t, router, user, "/", http.StatusOK)
}

func insertJourneyAdmin(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('journeyadmin', 'test', 'admin')`); err != nil {
		t.Fatal(err)
	}
}

func insertJourneyJam(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var jamID int64
	var jamPublic string
	if err = tx.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override, max_team_size)
		VALUES ('Journey Jam', 'draft', now()+interval '7 days', now()+interval '9 days', now()+interval '11 days', now()+interval '13 days', 'upcoming', 5)
		RETURNING id, public_id`).Scan(&jamID, &jamPublic); err != nil {
		t.Fatal(err)
	}
	insertJourneyQuestionnaire(t, ctx, tx, jamID)
	if _, err = tx.Exec(ctx, `INSERT INTO jam_themes (jam_id, phrase) VALUES ($1, $2), ($1, $3)`, jamID, journeyThemeOne, journeyThemeTwo); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return jamID, jamPublic
}

func insertJourneyQuestionnaire(t *testing.T, ctx context.Context, tx pgx.Tx, jamID int64) {
	t.Helper()
	var questionnaireID int64
	if err := tx.QueryRow(ctx, `INSERT INTO questionnaires (jam_id) VALUES ($1) RETURNING id`, jamID).Scan(&questionnaireID); err != nil {
		t.Fatal(err)
	}
	questions := []struct {
		typeName       string
		prompt         string
		required       bool
		textLimit      *int
		selectionLimit *int
	}{
		{"short_text", "Как вас зовут?", true, intPointer(100), nil},
		{"single_choice", "Выберите один вариант", true, nil, nil},
		{"multiple_choice", "Выберите варианты", true, nil, intPointer(2)},
	}
	questionIDs := make([]int64, 0, len(questions))
	for position, question := range questions {
		var questionID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, required, text_limit, selection_limit, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			questionnaireID, question.typeName, question.prompt, question.required, question.textLimit, question.selectionLimit, position).Scan(&questionID); err != nil {
			t.Fatal(err)
		}
		questionIDs = append(questionIDs, questionID)
	}
	for _, questionID := range questionIDs[1:] {
		if _, err := tx.Exec(ctx, `INSERT INTO questionnaire_options (question_id, label, position) VALUES ($1, 'Вариант один', 0), ($1, 'Вариант два', 1)`, questionID); err != nil {
			t.Fatal(err)
		}
	}
}

func intPointer(value int) *int {
	return &value
}

type journeyQuestionQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// journeyQuestionIDs reads question ids in position order through either the
// pool or a transaction.
func journeyQuestionIDs(t *testing.T, ctx context.Context, queryer journeyQuestionQueryer, jamID int64) []int64 {
	t.Helper()
	rows, err := queryer.Query(ctx, `
		SELECT question.id
		FROM questionnaire_questions question
		JOIN questionnaires questionnaire ON questionnaire.id = question.questionnaire_id
		WHERE questionnaire.jam_id = $1 AND question.revision = questionnaire.current_revision
		ORDER BY question.position`, jamID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func journeyCompleteQuestionnaire(t *testing.T, router http.Handler, user *journeyUser, jamPathPrefix string, questionShort, questionSingle, questionMultiple, singleOption, multipleOption int64) {
	t.Helper()
	journeyGet(t, router, user, jamPathPrefix+"/questionnaire", http.StatusOK)
	save := journeyPostJSON(t, router, user, jamPathPrefix+"/questionnaire/autosave", map[string]any{
		"question_id": questionShort, "value": journeyQuestionAnswer,
	}, http.StatusOK)
	if !strings.Contains(save.Body.String(), `"saved":true`) {
		t.Fatalf("autosave text answer body=%s", save.Body.String())
	}
	journeyPostJSON(t, router, user, jamPathPrefix+"/questionnaire/autosave", map[string]any{
		"question_id": questionSingle, "option_ids": []int64{singleOption},
	}, http.StatusOK)
	journeyPostJSON(t, router, user, jamPathPrefix+"/questionnaire/autosave", map[string]any{
		"question_id": questionMultiple, "option_ids": []int64{multipleOption},
	}, http.StatusOK)
	complete := journeyPostForm(t, router, user, jamPathPrefix+"/questionnaire/complete", url.Values{"csrf_token": {user.Jar.csrfCookie()}}, http.StatusSeeOther)
	if !strings.Contains(complete.Header().Get("Location"), "completed=1") {
		t.Fatalf("questionnaire completion redirect: %s", complete.Header().Get("Location"))
	}
}

func routePrefix(jamPublic string) string {
	return "/jams/" + jamPublic
}

func journeyTeamPublicFromLocation(t *testing.T, location string) string {
	t.Helper()
	match := teamPublicLocationPattern.FindStringSubmatch(location)
	if len(match) != 2 {
		t.Fatalf("team create redirect %q does not carry the team public id", location)
	}
	return match[1]
}

var teamPublicLocationPattern = regexp.MustCompile(`^/teams/([0-9a-f]{18})$`)

func journeyInviteToken(t *testing.T, body string) string {
	t.Helper()
	match := inviteTokenPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("invite issue page does not show a token: %s", body)
	}
	return match[1]
}

var inviteTokenPattern = regexp.MustCompile(`href="/invites/([A-Za-z0-9_-]+)"`)

func journeyProductPublic(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID int64) string {
	t.Helper()
	var publicID string
	if err := pool.QueryRow(ctx, `SELECT public_id FROM products WHERE jam_id=$1`, jamID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	return publicID
}

func journeyNominationPublic(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID int64) string {
	t.Helper()
	var publicID string
	if err := pool.QueryRow(ctx, `SELECT public_id FROM nominations WHERE jam_id=$1 AND kind='team' LIMIT 1`, jamID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	return publicID
}

func insertJourneyCuratorNomination(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Приз зрительских симпатий')`, jamID); err != nil {
		t.Fatal(err)
	}
}

func setJamOverride(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID int64, override string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE jams SET status_override=$2 WHERE id=$1`, jamID, override); err != nil {
		t.Fatal(err)
	}
}

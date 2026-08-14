package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const jamLifecycleLock int64 = 0x4a414d53

var errAdminInput = errors.New("invalid admin input")

type adminJam struct {
	ID              int64
	Title           string
	Description     string
	Rules           string
	Visibility      string
	Schedule        Schedule
	MaxTeamSize     int
	Stage           Stage
	QuestionCount   int
	SubmissionLocal string
	EvaluationLocal string
	VotingLocal     string
	FinishesLocal   string
}

type adminJamForm struct {
	Title            string
	Description      string
	Rules            string
	SubmissionStarts string
	EvaluationStarts string
	VotingStarts     string
	Finishes         string
	MaxTeamSize      string
	Reason           string
}

type adminQuestion struct {
	ID             int64
	Type           string
	Prompt         string
	Hint           string
	Required       bool
	TextLimit      int
	SelectionLimit int
	Position       int
	Options        []string
	OptionsText    string
	Reason         string
}

type adminQuestionForm struct {
	ID             int64
	Type           string
	Prompt         string
	Hint           string
	Required       bool
	TextLimit      string
	SelectionLimit string
	Position       string
	Options        string
	Reason         string
}

type jamAdminPageData struct {
	PageData
	Jams                []adminJam
	Jam                 *adminJam
	JamForm             adminJamForm
	Questions           []adminQuestion
	QuestionForm        adminQuestionForm
	EditingQuestion     bool
	QuestionnaireLocked bool
	MoscowZone          string
	Development         bool
}

// registerJamAdminRoutes installs the jam administration surface. The caller
// must invoke it while constructing the application's Gin router.
func (a *App) registerJamAdminRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("", a.jamAdminDashboard)
	admin.POST("/demo", a.createDemoJamAdmin)
	admin.GET("/jams/new", a.newJamAdminPage)
	admin.POST("/jams/new", a.createJamAdmin)
	admin.GET("/jams/:id/edit", a.editJamAdminPage)
	admin.POST("/jams/:id/edit", a.updateJamAdmin)
	admin.POST("/jams/:id/publish", a.publishJamAdmin)
	admin.POST("/jams/:id/unpublish", a.unpublishJamAdmin)
	admin.POST("/jams/:id/override", a.overrideJamAdmin)
	admin.POST("/jams/:id/auto", a.autoJamAdmin)
	admin.GET("/jams/:id/questionnaire", a.questionnaireAdminPage)
	admin.POST("/jams/:id/questionnaire/questions", a.createQuestionAdmin)
	admin.GET("/jams/:id/questionnaire/questions/:questionID/edit", a.editQuestionAdminPage)
	admin.POST("/jams/:id/questionnaire/questions/:questionID/edit", a.updateQuestionAdmin)
	admin.POST("/jams/:id/questionnaire/questions/:questionID/delete", a.deleteQuestionAdmin)
}

func (a *App) jamAdminDashboard(c *gin.Context) {
	jams, err := a.loadAdminJams(c.Request.Context())
	if err != nil {
		a.logger.Error("load admin jams", "error", err)
		a.renderJamAdmin(c, http.StatusInternalServerError, "admin_jams.html", jamAdminPageData{PageData: PageData{Error: "Не удалось загрузить джемы."}})
		return
	}
	a.renderJamAdmin(c, http.StatusOK, "admin_jams.html", jamAdminPageData{Jams: jams, Development: !a.config.Production(), PageData: PageData{Error: c.Query("error")}})
}

func (a *App) newJamAdminPage(c *gin.Context) {
	form := defaultAdminJamForm(time.Now())
	a.renderJamAdmin(c, http.StatusOK, "admin_jam_form.html", jamAdminPageData{JamForm: form, MoscowZone: "Europe/Moscow"})
}

func defaultAdminJamForm(now time.Time) adminJamForm {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		location = time.FixedZone("Europe/Moscow", 3*60*60)
	}
	format := func(value time.Time) string { return value.In(location).Format("2006-01-02T15:04") }
	submission := now.Add(7 * 24 * time.Hour)
	return adminJamForm{
		SubmissionStarts: format(submission),
		EvaluationStarts: format(submission.Add(7 * 24 * time.Hour)),
		VotingStarts:     format(submission.Add(9 * 24 * time.Hour)),
		Finishes:         format(submission.Add(11 * 24 * time.Hour)),
		MaxTeamSize:      "5",
		Reason:           "Создание нового джема",
	}
}

func (a *App) createDemoJamAdmin(c *gin.Context) {
	if a.config.Production() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	now := time.Now()
	schedule := Schedule{
		SubmissionStartsAt: now.Add(7 * 24 * time.Hour),
		EvaluationStartsAt: now.Add(14 * 24 * time.Hour),
		VotingStartsAt:     now.Add(16 * 24 * time.Hour),
		FinishesAt:         now.Add(18 * 24 * time.Hour),
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin demo jam creation", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock($1)`, jamLifecycleLock); err != nil {
		a.jamAdminFailure(c, "lock demo jam lifecycle", err)
		return
	}
	active, err := hasOtherActivePublishedJam(c.Request.Context(), tx, 0, now)
	if err != nil {
		a.jamAdminFailure(c, "check demo active jam", err)
		return
	}
	if active {
		c.Redirect(http.StatusSeeOther, "/admin?error="+urlQueryEscape("Уже существует опубликованный активный джем."))
		return
	}
	var jamID int64
	err = tx.QueryRow(c.Request.Context(), `
		INSERT INTO jams (title, description, rules, visibility, submission_starts_at,
		                  evaluation_starts_at, voting_starts_at, finishes_at, max_team_size)
		VALUES ('Тестовое дело', 'Проверка командной платформы перед настоящим джемом.',
		        'Создайте команду, пригласите участника и завершите тестовую анкету.', 'published',
		        $1, $2, $3, $4, 5) RETURNING id`,
		schedule.SubmissionStartsAt, schedule.EvaluationStartsAt, schedule.VotingStartsAt, schedule.FinishesAt).Scan(&jamID)
	if err != nil {
		a.jamAdminFailure(c, "insert demo jam", err)
		return
	}
	var questionnaireID int64
	if err = tx.QueryRow(c.Request.Context(), `INSERT INTO questionnaires (jam_id) VALUES ($1) RETURNING id`, jamID).Scan(&questionnaireID); err != nil {
		a.jamAdminFailure(c, "insert demo questionnaire", err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `
		INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, hint, required, text_limit, position)
		VALUES ($1, 'short_text', 'Что вы хотите попробовать на этом джеме?',
		        'Короткий тестовый ответ', true, 500, 0)`, questionnaireID); err != nil {
		a.jamAdminFailure(c, "insert demo question", err)
		return
	}
	after := map[string]any{"id": jamID, "title": "Тестовое дело", "visibility": "published", "demo": true}
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), "jam.demo_create", "jam", jamID, "Создание тестового джема в development", nil, after); err != nil {
		a.jamAdminFailure(c, "audit demo jam", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit demo jam", err)
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (a *App) createJamAdmin(c *gin.Context) {
	form := jamFormFromRequest(c)
	values, err := parseJamForm(form)
	if err != nil {
		a.renderJamAdmin(c, http.StatusUnprocessableEntity, "admin_jam_form.html", jamAdminPageData{PageData: PageData{Error: err.Error()}, JamForm: form, MoscowZone: "Europe/Moscow"})
		return
	}
	user := CurrentUser(c)
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin jam creation", err)
		return
	}
	defer tx.Rollback(c.Request.Context())

	var jamID int64
	err = tx.QueryRow(c.Request.Context(), `
		INSERT INTO jams (title, description, rules, submission_starts_at, evaluation_starts_at,
		                  voting_starts_at, finishes_at, max_team_size)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		values.Title, values.Description, values.Rules, values.Schedule.SubmissionStartsAt,
		values.Schedule.EvaluationStartsAt, values.Schedule.VotingStartsAt,
		values.Schedule.FinishesAt, values.MaxTeamSize).Scan(&jamID)
	if err != nil {
		a.jamAdminFailure(c, "insert jam", err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `INSERT INTO questionnaires (jam_id) VALUES ($1)`, jamID); err != nil {
		a.jamAdminFailure(c, "insert jam questionnaire", err)
		return
	}
	after := jamAuditData(jamID, values, "draft", nil)
	if err = insertAdminAudit(c.Request.Context(), tx, user, "jam.create", "jam", jamID, values.Reason, nil, after); err != nil {
		a.jamAdminFailure(c, "audit jam creation", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit jam creation", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/edit", jamID))
}

func (a *App) editJamAdminPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load jam for editing", err)
		return
	}
	a.renderJamAdmin(c, http.StatusOK, "admin_jam_form.html", jamAdminPageData{Jam: jam, JamForm: jamFormFromJam(*jam), MoscowZone: "Europe/Moscow"})
}

func (a *App) updateJamAdmin(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	form := jamFormFromRequest(c)
	values, err := parseJamForm(form)
	if err != nil {
		a.renderJamEditError(c, jamID, form, err.Error())
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin jam update", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock($1)`, jamLifecycleLock); err != nil {
		a.jamAdminFailure(c, "lock jam lifecycle", err)
		return
	}
	before, err := loadAdminJamTx(c.Request.Context(), tx, jamID, true)
	if err != nil {
		a.handleAdminLoadError(c, "lock jam for editing", err)
		return
	}
	var largestTeam int
	if err = tx.QueryRow(c.Request.Context(), `
		SELECT COALESCE(max(member_count), 0) FROM (
			SELECT count(*) AS member_count FROM team_members WHERE jam_id=$1 GROUP BY team_id
		) sizes`, jamID).Scan(&largestTeam); err != nil {
		a.jamAdminFailure(c, "check current team sizes", err)
		return
	}
	if values.MaxTeamSize < largestTeam {
		a.renderJamEditError(c, jamID, form, fmt.Sprintf("Нельзя установить лимит меньше текущего состава самой большой команды (%d).", largestTeam))
		return
	}
	scheduleChanged := !sameSchedule(before.Schedule, values.Schedule)
	if scheduleChanged {
		resultingSchedule := values.Schedule
		resultingSchedule.Override = before.Schedule.Override
		if StageAtLeast(EffectiveStage(resultingSchedule, time.Now()), StageSubmission) {
			hasTheme, themeErr := hasActiveTheme(c.Request.Context(), tx, jamID)
			if themeErr != nil {
				a.jamAdminFailure(c, "check themes during schedule update", themeErr)
				return
			}
			if !hasTheme {
				a.renderJamEditError(c, jamID, form, "Для расписания, переводящего джем в submission или позже, нужна хотя бы одна активная тема.")
				return
			}
		}
		if before.Visibility == "published" {
			active, checkErr := hasOtherActivePublishedJam(c.Request.Context(), tx, jamID, time.Now())
			if checkErr != nil {
				a.jamAdminFailure(c, "check active jam during schedule update", checkErr)
				return
			}
			if active {
				a.renderJamEditError(c, jamID, form, "Изменение реактивирует джем, пока уже опубликован другой активный джем.")
				return
			}
		}
	}
	_, err = tx.Exec(c.Request.Context(), `
		UPDATE jams SET title=$2, description=$3, rules=$4, submission_starts_at=$5,
		       evaluation_starts_at=$6, voting_starts_at=$7, finishes_at=$8,
		       max_team_size=$9, updated_at=now() WHERE id=$1`,
		jamID, values.Title, values.Description, values.Rules, values.Schedule.SubmissionStartsAt,
		values.Schedule.EvaluationStartsAt, values.Schedule.VotingStartsAt,
		values.Schedule.FinishesAt, values.MaxTeamSize)
	if err != nil {
		a.jamAdminFailure(c, "update jam", err)
		return
	}
	after := jamAuditData(jamID, values, before.Visibility, before.Schedule.Override)
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), "jam.update", "jam", jamID, values.Reason, jamRecordAuditData(*before), after); err != nil {
		a.jamAdminFailure(c, "audit jam update", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit jam update", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/edit", jamID))
}

func (a *App) publishJamAdmin(c *gin.Context) {
	a.setJamVisibility(c, "published")
}

func (a *App) unpublishJamAdmin(c *gin.Context) {
	a.setJamVisibility(c, "draft")
}

func (a *App) setJamVisibility(c *gin.Context, visibility string) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderJamActionError(c, jamID, err.Error())
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin visibility update", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock($1)`, jamLifecycleLock); err != nil {
		a.jamAdminFailure(c, "lock jam lifecycle", err)
		return
	}
	jam, err := loadAdminJamTx(c.Request.Context(), tx, jamID, true)
	if err != nil {
		a.handleAdminLoadError(c, "lock jam visibility", err)
		return
	}
	if jam.Visibility == visibility {
		a.renderJamActionError(c, jamID, "Видимость джема уже имеет выбранное значение.")
		return
	}
	if visibility == "published" {
		if jam.QuestionCount < 1 {
			a.renderJamActionError(c, jamID, "Перед публикацией добавьте хотя бы один вопрос анкеты.")
			return
		}
		if EffectiveStage(jam.Schedule, time.Now()) != StageUpcoming {
			a.renderJamActionError(c, jamID, "Публиковать можно только джем на стадии upcoming.")
			return
		}
		active, checkErr := hasOtherActivePublishedJam(c.Request.Context(), tx, jamID, time.Now())
		if checkErr != nil {
			a.jamAdminFailure(c, "check active jam before publication", checkErr)
			return
		}
		if active {
			a.renderJamActionError(c, jamID, "Уже существует другой опубликованный активный джем.")
			return
		}
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE jams SET visibility=$2, updated_at=now() WHERE id=$1`, jamID, visibility); err != nil {
		a.jamAdminFailure(c, "update jam visibility", err)
		return
	}
	after := jamRecordAuditData(*jam)
	after["visibility"] = visibility
	action := "jam.publish"
	if visibility == "draft" {
		action = "jam.unpublish"
	}
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), action, "jam", jamID, reason, jamRecordAuditData(*jam), after); err != nil {
		a.jamAdminFailure(c, "audit jam visibility", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit jam visibility", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/edit", jamID))
}

func (a *App) overrideJamAdmin(c *gin.Context) {
	stage := Stage(strings.TrimSpace(c.PostForm("stage")))
	if stage != StageUpcoming && stage != StageSubmission && stage != StageEvaluation && stage != StageVoting && stage != StageFinished {
		jamID, ok := adminID(c, "id")
		if ok {
			a.renderJamActionError(c, jamID, "Выберите допустимую стадию.")
		}
		return
	}
	a.setJamOverride(c, &stage)
}

func (a *App) autoJamAdmin(c *gin.Context) {
	a.setJamOverride(c, nil)
}

func (a *App) setJamOverride(c *gin.Context, override *Stage) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderJamActionError(c, jamID, err.Error())
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin jam override", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock($1)`, jamLifecycleLock); err != nil {
		a.jamAdminFailure(c, "lock jam lifecycle", err)
		return
	}
	jam, err := loadAdminJamTx(c.Request.Context(), tx, jamID, true)
	if err != nil {
		a.handleAdminLoadError(c, "lock jam override", err)
		return
	}
	if sameStagePointer(jam.Schedule.Override, override) {
		a.renderJamActionError(c, jamID, "Выбранный режим стадии уже установлен.")
		return
	}
	resulting := jam.Schedule
	resulting.Override = override
	if StageAtLeast(EffectiveStage(resulting, time.Now()), StageSubmission) {
		hasTheme, themeErr := hasActiveTheme(c.Request.Context(), tx, jamID)
		if themeErr != nil {
			a.jamAdminFailure(c, "check themes before stage change", themeErr)
			return
		}
		if !hasTheme {
			a.renderJamActionError(c, jamID, "Для стадии submission или позже нужна хотя бы одна активная тема.")
			return
		}
	}
	if jam.Visibility == "published" && EffectiveStage(resulting, time.Now()) != StageFinished {
		active, checkErr := hasOtherActivePublishedJam(c.Request.Context(), tx, jamID, time.Now())
		if checkErr != nil {
			a.jamAdminFailure(c, "check active jam before override", checkErr)
			return
		}
		if active {
			a.renderJamActionError(c, jamID, "Изменение стадии реактивирует джем, пока уже опубликован другой активный джем.")
			return
		}
	}
	var value any
	if override != nil {
		value = string(*override)
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE jams SET status_override=$2, updated_at=now() WHERE id=$1`, jamID, value); err != nil {
		a.jamAdminFailure(c, "update jam override", err)
		return
	}
	after := jamRecordAuditData(*jam)
	after["status_override"] = value
	action := "jam.override"
	if override == nil {
		action = "jam.auto_stage"
	}
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), action, "jam", jamID, reason, jamRecordAuditData(*jam), after); err != nil {
		a.jamAdminFailure(c, "audit jam override", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit jam override", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/edit", jamID))
}

func (a *App) questionnaireAdminPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	a.renderQuestionnaireAdmin(c, jamID, adminQuestionForm{Type: "short_text", TextLimit: "500"}, false, "", http.StatusOK)
}

func (a *App) editQuestionAdminPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	questionID, ok := adminID(c, "questionID")
	if !ok {
		return
	}
	question, err := a.loadAdminQuestion(c.Request.Context(), jamID, questionID)
	if err != nil {
		a.handleAdminLoadError(c, "load questionnaire question", err)
		return
	}
	form := questionFormFromQuestion(*question)
	a.renderQuestionnaireAdmin(c, jamID, form, true, "", http.StatusOK)
}

func (a *App) createQuestionAdmin(c *gin.Context) {
	a.mutateQuestionAdmin(c, false)
}

func (a *App) updateQuestionAdmin(c *gin.Context) {
	a.mutateQuestionAdmin(c, true)
}

func (a *App) mutateQuestionAdmin(c *gin.Context, editing bool) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	form := questionFormFromRequest(c)
	if editing {
		form.ID, ok = adminID(c, "questionID")
		if !ok {
			return
		}
	}
	question, err := parseQuestionForm(form)
	if err != nil {
		a.renderQuestionnaireAdmin(c, jamID, form, editing, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin questionnaire update", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	questionnaireID, locked, err := lockEditableQuestionnaire(c.Request.Context(), tx, jamID)
	if err != nil {
		if errors.Is(err, errAdminInput) {
			tx.Rollback(c.Request.Context())
			a.renderQuestionnaireAdmin(c, jamID, form, editing, "Анкету можно структурно изменять только у draft-джема.", http.StatusConflict)
			return
		}
		a.handleAdminLoadError(c, "lock questionnaire", err)
		return
	}
	if locked {
		tx.Rollback(c.Request.Context())
		a.renderQuestionnaireAdmin(c, jamID, form, editing, "Структурные изменения заблокированы после первого сохранённого ответа.", http.StatusConflict)
		return
	}

	var before any
	var questionID int64
	if editing {
		existing, loadErr := loadAdminQuestionTx(c.Request.Context(), tx, questionnaireID, form.ID, true)
		if loadErr != nil {
			a.handleAdminLoadError(c, "lock questionnaire question", loadErr)
			return
		}
		before = questionAuditData(*existing)
		questionID = existing.ID
		if _, err = tx.Exec(c.Request.Context(), `DELETE FROM questionnaire_options WHERE question_id=$1`, questionID); err != nil {
			a.jamAdminFailure(c, "replace question options", err)
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `
			UPDATE questionnaire_questions SET type=$2, prompt=$3, hint=$4, required=$5,
			       text_limit=$6, selection_limit=$7, updated_at=now() WHERE id=$1`,
			questionID, question.Type, question.Prompt, nullableString(question.Hint), question.Required,
			nullablePositive(question.TextLimit), nullablePositive(question.SelectionLimit)); err != nil {
			a.jamAdminFailure(c, "update questionnaire question", err)
			return
		}
		question.Position, err = moveQuestion(c.Request.Context(), tx, questionnaireID, questionID, question.Position)
		if err != nil {
			a.jamAdminFailure(c, "move questionnaire question", err)
			return
		}
	} else {
		err = tx.QueryRow(c.Request.Context(), `
			INSERT INTO questionnaire_questions (questionnaire_id, type, prompt, hint, required,
			       text_limit, selection_limit, position)
			SELECT $1, $2, $3, $4, $5, $6, $7, COALESCE(max(position)+1, 0)
			FROM questionnaire_questions WHERE questionnaire_id=$1 RETURNING id, position`,
			questionnaireID, question.Type, question.Prompt, nullableString(question.Hint), question.Required,
			nullablePositive(question.TextLimit), nullablePositive(question.SelectionLimit)).Scan(&questionID, &question.Position)
		if err != nil {
			a.jamAdminFailure(c, "insert questionnaire question", err)
			return
		}
	}
	for position, option := range question.Options {
		if _, err = tx.Exec(c.Request.Context(), `INSERT INTO questionnaire_options (question_id, label, position) VALUES ($1, $2, $3)`, questionID, option, position); err != nil {
			a.jamAdminFailure(c, "insert questionnaire option", err)
			return
		}
	}
	question.ID = questionID
	action := "questionnaire.question_create"
	if editing {
		action = "questionnaire.question_update"
	}
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), action, "questionnaire_question", questionID, question.Reason, before, questionAuditData(question)); err != nil {
		a.jamAdminFailure(c, "audit questionnaire question", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit questionnaire question", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/questionnaire", jamID))
}

func (a *App) deleteQuestionAdmin(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	questionID, ok := adminID(c, "questionID")
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderQuestionnaireAdmin(c, jamID, adminQuestionForm{}, false, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.jamAdminFailure(c, "begin question deletion", err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	questionnaireID, locked, err := lockEditableQuestionnaire(c.Request.Context(), tx, jamID)
	if err != nil {
		if errors.Is(err, errAdminInput) {
			tx.Rollback(c.Request.Context())
			a.renderQuestionnaireAdmin(c, jamID, adminQuestionForm{}, false, "Анкету можно структурно изменять только у draft-джема.", http.StatusConflict)
			return
		}
		a.handleAdminLoadError(c, "lock questionnaire for deletion", err)
		return
	}
	if locked {
		tx.Rollback(c.Request.Context())
		a.renderQuestionnaireAdmin(c, jamID, adminQuestionForm{}, false, "Удаление заблокировано после первого сохранённого ответа.", http.StatusConflict)
		return
	}
	question, err := loadAdminQuestionTx(c.Request.Context(), tx, questionnaireID, questionID, true)
	if err != nil {
		a.handleAdminLoadError(c, "lock question for deletion", err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM questionnaire_options WHERE question_id=$1`, questionID); err != nil {
		a.jamAdminFailure(c, "delete question options", err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM questionnaire_questions WHERE id=$1`, questionID); err != nil {
		a.jamAdminFailure(c, "delete questionnaire question", err)
		return
	}
	if err = insertAdminAudit(c.Request.Context(), tx, CurrentUser(c), "questionnaire.question_delete", "questionnaire_question", questionID, reason, questionAuditData(*question), nil); err != nil {
		a.jamAdminFailure(c, "audit question deletion", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.jamAdminFailure(c, "commit question deletion", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/questionnaire", jamID))
}

func (a *App) renderJamAdmin(c *gin.Context, status int, name string, data jamAdminPageData) {
	data.User = CurrentUser(c)
	data.CSRFToken = csrfToken(c)
	c.HTML(status, name, data)
}

func (a *App) renderJamEditError(c *gin.Context, jamID int64, form adminJamForm, message string) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "reload jam after validation error", err)
		return
	}
	a.renderJamAdmin(c, http.StatusUnprocessableEntity, "admin_jam_form.html", jamAdminPageData{PageData: PageData{Error: message}, Jam: jam, JamForm: form, MoscowZone: "Europe/Moscow"})
}

func (a *App) renderJamActionError(c *gin.Context, jamID int64, message string) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "reload jam after action error", err)
		return
	}
	a.renderJamAdmin(c, http.StatusConflict, "admin_jam_form.html", jamAdminPageData{PageData: PageData{Error: message}, Jam: jam, JamForm: jamFormFromJam(*jam), MoscowZone: "Europe/Moscow"})
}

func (a *App) renderQuestionnaireAdmin(c *gin.Context, jamID int64, form adminQuestionForm, editing bool, message string, status int) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load jam questionnaire", err)
		return
	}
	questions, locked, err := a.loadAdminQuestions(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load questionnaire builder", err)
		return
	}
	if form.Type == "" {
		form.Type = "short_text"
		form.TextLimit = "500"
	}
	a.renderJamAdmin(c, status, "admin_questionnaire.html", jamAdminPageData{
		PageData: PageData{Error: message}, Jam: jam, Questions: questions,
		QuestionForm: form, EditingQuestion: editing, QuestionnaireLocked: locked,
	})
}

func (a *App) loadAdminJams(ctx context.Context) ([]adminJam, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT j.id, j.title, j.description, j.rules, j.visibility, j.submission_starts_at,
		       j.evaluation_starts_at, j.voting_starts_at, j.finishes_at, j.status_override,
		       j.max_team_size, count(qn.id)
		FROM jams j
		LEFT JOIN questionnaires q ON q.jam_id=j.id
		LEFT JOIN questionnaire_questions qn ON qn.questionnaire_id=q.id
		GROUP BY j.id ORDER BY j.created_at DESC, j.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jams []adminJam
	for rows.Next() {
		jam, scanErr := scanAdminJam(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jams = append(jams, *jam)
	}
	return jams, rows.Err()
}

func (a *App) loadAdminJam(ctx context.Context, jamID int64) (*adminJam, error) {
	return scanAdminJam(a.pool.QueryRow(ctx, adminJamSQL(false), jamID))
}

func loadAdminJamTx(ctx context.Context, tx pgx.Tx, jamID int64, lock bool) (*adminJam, error) {
	return scanAdminJam(tx.QueryRow(ctx, adminJamSQL(lock), jamID))
}

func adminJamSQL(lock bool) string {
	query := `
		SELECT j.id, j.title, j.description, j.rules, j.visibility, j.submission_starts_at,
		       j.evaluation_starts_at, j.voting_starts_at, j.finishes_at, j.status_override,
		       j.max_team_size,
		       (SELECT count(*) FROM questionnaire_questions qn
		        JOIN questionnaires q ON q.id=qn.questionnaire_id WHERE q.jam_id=j.id)
		FROM jams j WHERE j.id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	return query
}

type rowScanner interface {
	Scan(...any) error
}

func scanAdminJam(row rowScanner) (*adminJam, error) {
	var jam adminJam
	var override *string
	err := row.Scan(&jam.ID, &jam.Title, &jam.Description, &jam.Rules, &jam.Visibility,
		&jam.Schedule.SubmissionStartsAt, &jam.Schedule.EvaluationStartsAt,
		&jam.Schedule.VotingStartsAt, &jam.Schedule.FinishesAt, &override,
		&jam.MaxTeamSize, &jam.QuestionCount)
	if err != nil {
		return nil, err
	}
	if override != nil {
		stage := Stage(*override)
		jam.Schedule.Override = &stage
	}
	jam.Stage = EffectiveStage(jam.Schedule, time.Now())
	if location, locationErr := time.LoadLocation("Europe/Moscow"); locationErr == nil {
		jam.SubmissionLocal = jam.Schedule.SubmissionStartsAt.In(location).Format("2006-01-02T15:04")
		jam.EvaluationLocal = jam.Schedule.EvaluationStartsAt.In(location).Format("2006-01-02T15:04")
		jam.VotingLocal = jam.Schedule.VotingStartsAt.In(location).Format("2006-01-02T15:04")
		jam.FinishesLocal = jam.Schedule.FinishesAt.In(location).Format("2006-01-02T15:04")
	}
	return &jam, nil
}

func (a *App) loadAdminQuestions(ctx context.Context, jamID int64) ([]adminQuestion, bool, error) {
	var questionnaireID int64
	var visibility string
	err := a.pool.QueryRow(ctx, `SELECT q.id, j.visibility FROM questionnaires q JOIN jams j ON j.id=q.jam_id WHERE j.id=$1`, jamID).Scan(&questionnaireID, &visibility)
	if err != nil {
		return nil, false, err
	}
	locked, err := questionnaireHasAnswers(ctx, a.pool, questionnaireID)
	if err != nil {
		return nil, false, err
	}
	locked = locked || visibility != "draft"
	rows, err := a.pool.Query(ctx, `
		SELECT q.id, q.type, q.prompt, COALESCE(q.hint, ''), q.required,
		       COALESCE(q.text_limit, 0), COALESCE(q.selection_limit, 0), q.position,
		       COALESCE(array_agg(o.label ORDER BY o.position) FILTER (WHERE o.id IS NOT NULL), ARRAY[]::varchar[])
		FROM questionnaire_questions q
		LEFT JOIN questionnaire_options o ON o.question_id=q.id
		WHERE q.questionnaire_id=$1
		GROUP BY q.id ORDER BY q.position`, questionnaireID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var questions []adminQuestion
	for rows.Next() {
		var question adminQuestion
		if err = rows.Scan(&question.ID, &question.Type, &question.Prompt, &question.Hint, &question.Required, &question.TextLimit, &question.SelectionLimit, &question.Position, &question.Options); err != nil {
			return nil, false, err
		}
		question.OptionsText = strings.Join(question.Options, "\n")
		questions = append(questions, question)
	}
	return questions, locked, rows.Err()
}

func (a *App) loadAdminQuestion(ctx context.Context, jamID, questionID int64) (*adminQuestion, error) {
	var questionnaireID int64
	if err := a.pool.QueryRow(ctx, `SELECT id FROM questionnaires WHERE jam_id=$1`, jamID).Scan(&questionnaireID); err != nil {
		return nil, err
	}
	return loadAdminQuestionTx(ctx, a.pool, questionnaireID, questionID, false)
}

type queryDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadAdminQuestionTx(ctx context.Context, db queryDB, questionnaireID, questionID int64, lock bool) (*adminQuestion, error) {
	query := `SELECT id, type, prompt, COALESCE(hint, ''), required, COALESCE(text_limit, 0), COALESCE(selection_limit, 0), position FROM questionnaire_questions WHERE questionnaire_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var question adminQuestion
	err := db.QueryRow(ctx, query, questionnaireID, questionID).Scan(&question.ID, &question.Type, &question.Prompt, &question.Hint, &question.Required, &question.TextLimit, &question.SelectionLimit, &question.Position)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT label FROM questionnaire_options WHERE question_id=$1 ORDER BY position`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var option string
		if err = rows.Scan(&option); err != nil {
			return nil, err
		}
		question.Options = append(question.Options, option)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	question.OptionsText = strings.Join(question.Options, "\n")
	return &question, nil
}

func lockEditableQuestionnaire(ctx context.Context, tx pgx.Tx, jamID int64) (int64, bool, error) {
	var questionnaireID int64
	var visibility string
	err := tx.QueryRow(ctx, `SELECT q.id, j.visibility FROM questionnaires q JOIN jams j ON j.id=q.jam_id WHERE j.id=$1 FOR UPDATE OF q, j`, jamID).Scan(&questionnaireID, &visibility)
	if err != nil {
		return 0, false, err
	}
	if visibility != "draft" {
		return 0, false, errAdminInput
	}
	locked, err := questionnaireHasAnswers(ctx, tx, questionnaireID)
	return questionnaireID, locked, err
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func questionnaireHasAnswers(ctx context.Context, db queryRower, questionnaireID int64) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questionnaire_responses WHERE questionnaire_id=$1)`, questionnaireID).Scan(&exists)
	return exists, err
}

func moveQuestion(ctx context.Context, tx pgx.Tx, questionnaireID, questionID int64, requested int) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM questionnaire_questions WHERE questionnaire_id=$1 ORDER BY position`, questionnaireID)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if id != questionID {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if requested < 0 {
		requested = 0
	}
	if requested > len(ids) {
		requested = len(ids)
	}
	ids = append(ids, 0)
	copy(ids[requested+1:], ids[requested:])
	ids[requested] = questionID
	// Move positions out of the target range first to avoid transient UNIQUE collisions.
	if _, err = tx.Exec(ctx, `UPDATE questionnaire_questions SET position=position+1000000 WHERE questionnaire_id=$1`, questionnaireID); err != nil {
		return 0, err
	}
	for position, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE questionnaire_questions SET position=$2, updated_at=now() WHERE id=$1`, id, position); err != nil {
			return 0, err
		}
	}
	return requested, nil
}

func hasOtherActivePublishedJam(ctx context.Context, tx pgx.Tx, jamID int64, now time.Time) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM jams WHERE visibility='published' AND id<>$1 AND
			CASE
				WHEN status_override IS NOT NULL THEN status_override
				WHEN $2 < submission_starts_at THEN 'upcoming'
				WHEN $2 < evaluation_starts_at THEN 'submission'
				WHEN $2 < voting_starts_at THEN 'evaluation'
				WHEN $2 < finishes_at THEN 'voting'
				ELSE 'finished'
			END <> 'finished'
		)`, jamID, now).Scan(&exists)
	return exists, err
}

func insertAdminAudit(ctx context.Context, tx pgx.Tx, user *User, action, entityType string, entityID int64, reason string, before, after any) error {
	if user == nil {
		return errors.New("missing administrator in request context")
	}
	var currentRole string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 FOR KEY SHARE`, user.ID).Scan(&currentRole); err != nil {
		return err
	}
	if currentRole != "admin" {
		return errors.New("administrator role was revoked during the request")
	}
	beforeJSON, err := auditJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := auditJSON(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_audit_log (admin_user_id, action, entity_type, entity_id, reason, before_data, after_data)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb)`,
		user.ID, action, entityType, entityID, reason, beforeJSON, afterJSON)
	return err
}

func auditJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func jamAuditData(jamID int64, form adminJamFormValues, visibility string, override *Stage) map[string]any {
	var overrideValue any
	if override != nil {
		overrideValue = string(*override)
	}
	return map[string]any{
		"id": jamID, "title": form.Title, "description": form.Description, "rules": form.Rules,
		"visibility": visibility, "submission_starts_at": form.Schedule.SubmissionStartsAt,
		"evaluation_starts_at": form.Schedule.EvaluationStartsAt, "voting_starts_at": form.Schedule.VotingStartsAt,
		"finishes_at": form.Schedule.FinishesAt, "status_override": overrideValue, "max_team_size": form.MaxTeamSize,
	}
}

func jamRecordAuditData(jam adminJam) map[string]any {
	values := adminJamFormValues{Title: jam.Title, Description: jam.Description, Rules: jam.Rules, Schedule: jam.Schedule, MaxTeamSize: jam.MaxTeamSize}
	return jamAuditData(jam.ID, values, jam.Visibility, jam.Schedule.Override)
}

func questionAuditData(question adminQuestion) map[string]any {
	return map[string]any{"id": question.ID, "type": question.Type, "prompt": question.Prompt, "hint": question.Hint,
		"required": question.Required, "text_limit": nullablePositive(question.TextLimit),
		"selection_limit": nullablePositive(question.SelectionLimit), "position": question.Position, "options": question.Options}
}

type adminJamFormValues struct {
	Title       string
	Description string
	Rules       string
	Schedule    Schedule
	MaxTeamSize int
	Reason      string
}

func parseJamForm(form adminJamForm) (adminJamFormValues, error) {
	var values adminJamFormValues
	values.Title = strings.TrimSpace(form.Title)
	values.Description = strings.TrimSpace(form.Description)
	values.Rules = strings.TrimSpace(form.Rules)
	if utf8.RuneCountInString(values.Title) < 1 || utf8.RuneCountInString(values.Title) > 160 || hasControl(values.Title) {
		return values, errors.New("Название должно содержать от 1 до 160 символов без управляющих знаков.")
	}
	if utf8.RuneCountInString(values.Description) > 10000 || utf8.RuneCountInString(values.Rules) > 20000 {
		return values, errors.New("Описание или правила превышают допустимую длину.")
	}
	maxTeamSize, err := strconv.Atoi(strings.TrimSpace(form.MaxTeamSize))
	if err != nil || maxTeamSize < 1 || maxTeamSize > 100 {
		return values, errors.New("Максимальный размер команды должен быть от 1 до 100.")
	}
	values.MaxTeamSize = maxTeamSize
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return values, errors.New("Часовой пояс Europe/Moscow недоступен на сервере.")
	}
	parse := func(value string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(value), location)
	}
	if values.Schedule.SubmissionStartsAt, err = parse(form.SubmissionStarts); err != nil {
		return values, errors.New("Укажите корректное начало приёма работ по московскому времени.")
	}
	if values.Schedule.EvaluationStartsAt, err = parse(form.EvaluationStarts); err != nil {
		return values, errors.New("Укажите корректное начало оценки по московскому времени.")
	}
	if values.Schedule.VotingStartsAt, err = parse(form.VotingStarts); err != nil {
		return values, errors.New("Укажите корректное начало голосования по московскому времени.")
	}
	if values.Schedule.FinishesAt, err = parse(form.Finishes); err != nil {
		return values, errors.New("Укажите корректное время завершения по московскому времени.")
	}
	if !values.Schedule.SubmissionStartsAt.Before(values.Schedule.EvaluationStartsAt) ||
		!values.Schedule.EvaluationStartsAt.Before(values.Schedule.VotingStartsAt) ||
		!values.Schedule.VotingStartsAt.Before(values.Schedule.FinishesAt) {
		return values, errors.New("Границы расписания должны идти строго по порядку.")
	}
	values.Reason, err = validateReason(form.Reason)
	if err != nil {
		return values, err
	}
	return values, nil
}

func parseQuestionForm(form adminQuestionForm) (adminQuestion, error) {
	var question adminQuestion
	question.ID = form.ID
	question.Type = strings.TrimSpace(form.Type)
	question.Prompt = strings.TrimSpace(form.Prompt)
	question.Hint = strings.TrimSpace(form.Hint)
	question.Required = form.Required
	if utf8.RuneCountInString(question.Prompt) < 1 || utf8.RuneCountInString(question.Prompt) > 500 || hasControl(question.Prompt) {
		return question, errors.New("Формулировка должна содержать от 1 до 500 символов без управляющих знаков.")
	}
	if utf8.RuneCountInString(question.Hint) > 1000 {
		return question, errors.New("Подсказка не должна превышать 1000 символов.")
	}
	position, err := strconv.Atoi(strings.TrimSpace(form.Position))
	if form.Position == "" {
		position = 0
		err = nil
	}
	if err != nil || position < 0 {
		return question, errors.New("Позиция вопроса должна быть неотрицательным числом.")
	}
	question.Position = position
	for _, line := range strings.Split(strings.ReplaceAll(form.Options, "\r\n", "\n"), "\n") {
		option := strings.TrimSpace(line)
		if option == "" {
			continue
		}
		if utf8.RuneCountInString(option) > 300 || hasControl(option) {
			return question, errors.New("Каждый вариант должен содержать не более 300 символов без управляющих знаков.")
		}
		question.Options = append(question.Options, option)
	}
	if len(question.Options) > 100 {
		return question, errors.New("Допускается не более 100 вариантов ответа.")
	}
	switch question.Type {
	case "short_text":
		question.TextLimit, err = strconv.Atoi(strings.TrimSpace(form.TextLimit))
		if err != nil || question.TextLimit < 1 || question.TextLimit > 5000 {
			return question, errors.New("Лимит текста должен быть от 1 до 5000 символов.")
		}
		if len(question.Options) != 0 {
			return question, errors.New("Текстовый вопрос не должен содержать варианты ответа.")
		}
	case "single_choice":
		if len(question.Options) < 2 {
			return question, errors.New("Для одиночного выбора укажите хотя бы два варианта.")
		}
	case "multiple_choice":
		if len(question.Options) < 2 {
			return question, errors.New("Для множественного выбора укажите хотя бы два варианта.")
		}
		question.SelectionLimit, err = strconv.Atoi(strings.TrimSpace(form.SelectionLimit))
		if err != nil || question.SelectionLimit < 1 || question.SelectionLimit > len(question.Options) {
			return question, errors.New("Лимит выбора должен быть от 1 до количества вариантов.")
		}
	default:
		return question, errors.New("Выберите поддерживаемый тип вопроса.")
	}
	question.Reason, err = validateReason(form.Reason)
	if err != nil {
		return question, err
	}
	return question, nil
}

func validateReason(value string) (string, error) {
	reason := strings.TrimSpace(value)
	if utf8.RuneCountInString(reason) < 3 || utf8.RuneCountInString(reason) > 1000 {
		return "", errors.New("Причина должна содержать от 3 до 1000 символов.")
	}
	return reason, nil
}

func jamFormFromRequest(c *gin.Context) adminJamForm {
	return adminJamForm{Title: c.PostForm("title"), Description: c.PostForm("description"), Rules: c.PostForm("rules"),
		SubmissionStarts: c.PostForm("submission_starts_at"), EvaluationStarts: c.PostForm("evaluation_starts_at"),
		VotingStarts: c.PostForm("voting_starts_at"), Finishes: c.PostForm("finishes_at"),
		MaxTeamSize: c.PostForm("max_team_size"), Reason: c.PostForm("reason")}
}

func jamFormFromJam(jam adminJam) adminJamForm {
	return adminJamForm{Title: jam.Title, Description: jam.Description, Rules: jam.Rules,
		SubmissionStarts: jam.SubmissionLocal, EvaluationStarts: jam.EvaluationLocal,
		VotingStarts: jam.VotingLocal, Finishes: jam.FinishesLocal, MaxTeamSize: strconv.Itoa(jam.MaxTeamSize)}
}

func questionFormFromRequest(c *gin.Context) adminQuestionForm {
	return adminQuestionForm{Type: c.PostForm("type"), Prompt: c.PostForm("prompt"), Hint: c.PostForm("hint"),
		Required: c.PostForm("required") == "on", TextLimit: c.PostForm("text_limit"),
		SelectionLimit: c.PostForm("selection_limit"), Position: c.PostForm("position"),
		Options: c.PostForm("options"), Reason: c.PostForm("reason")}
}

func questionFormFromQuestion(question adminQuestion) adminQuestionForm {
	form := adminQuestionForm{ID: question.ID, Type: question.Type, Prompt: question.Prompt, Hint: question.Hint,
		Required: question.Required, Position: strconv.Itoa(question.Position), Options: question.OptionsText}
	if question.TextLimit > 0 {
		form.TextLimit = strconv.Itoa(question.TextLimit)
	}
	if question.SelectionLimit > 0 {
		form.SelectionLimit = strconv.Itoa(question.SelectionLimit)
	}
	return form
}

func adminID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		c.AbortWithStatus(http.StatusNotFound)
		return 0, false
	}
	return id, true
}

func sameSchedule(left, right Schedule) bool {
	return left.SubmissionStartsAt.Equal(right.SubmissionStartsAt) && left.EvaluationStartsAt.Equal(right.EvaluationStartsAt) &&
		left.VotingStartsAt.Equal(right.VotingStartsAt) && left.FinishesAt.Equal(right.FinishesAt)
}

func sameStagePointer(left, right *Stage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositive(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (a *App) handleAdminLoadError(c *gin.Context, operation string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	a.jamAdminFailure(c, operation, err)
}

func (a *App) jamAdminFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.String(http.StatusInternalServerError, "Не удалось выполнить административное действие.")
}

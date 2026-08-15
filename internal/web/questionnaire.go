package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type questionnaireAccess struct {
	JamID           int64
	JamTitle        string
	QuestionnaireID int64
	Revision        int
	TeamID          int64
	Stage           Stage
}

type questionnaireOptionView struct {
	ID       int64
	Label    string
	Selected bool
}

type questionnaireQuestionView struct {
	ID             int64
	Type           string
	Prompt         string
	Hint           string
	Required       bool
	TextLimit      int
	SelectionLimit int
	TextValue      string
	Options        []questionnaireOptionView
}

type questionnaireMemberView struct {
	Username string
	Status   string
}

type questionnairePageData struct {
	User           *User
	CSRFToken      string
	JamID          int64
	PublicJamID    string
	JamTitle       string
	Stage          Stage
	Writable       bool
	ResponseStatus string
	Questions      []questionnaireQuestionView
	Members        []questionnaireMemberView
	Error          string
	Completed      bool
}

type questionnaireAutosaveInput struct {
	QuestionID int64   `json:"question_id"`
	Value      *string `json:"value"`
	OptionIDs  []int64 `json:"option_ids"`
}

type questionnaireQuestionRule struct {
	ID             int64
	Type           string
	Prompt         string
	Position       int
	Required       bool
	TextLimit      int
	SelectionLimit int
}

type questionnaireSnapshot struct {
	Answers []questionnaireSnapshotAnswer `json:"answers"`
}

type questionnaireSnapshotAnswer struct {
	QuestionID   int64    `json:"question_id"`
	Type         string   `json:"type"`
	Prompt       string   `json:"prompt,omitempty"`
	Position     int      `json:"position,omitempty"`
	Text         *string  `json:"text,omitempty"`
	OptionIDs    []int64  `json:"option_ids,omitempty"`
	OptionLabels []string `json:"option_labels,omitempty"`
}

type questionnaireValidationError struct {
	message string
}

func (e *questionnaireValidationError) Error() string { return e.message }

func (a *App) registerQuestionnaireRoutes(router *gin.Engine) {
	router.GET("/jams/:id/questionnaire", RequireAuth(), a.questionnairePage)
	router.POST("/jams/:id/questionnaire/autosave", RequireAuth(), a.questionnaireAutosave)
	router.POST("/jams/:id/questionnaire/complete", RequireAuth(), a.questionnaireComplete)
}

func (a *App) questionnairePage(c *gin.Context) {
	jamID, ok := a.resolvePublicID(c, "id", "jams")
	if !ok {
		return
	}
	user := CurrentUser(c)
	access, err := a.questionnaireLoadAccess(c.Request.Context(), a.pool, jamID, user.ID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load questionnaire access", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if access.Stage != StageUpcoming {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	questions, status, err := a.questionnaireLoadOwnAnswers(c.Request.Context(), access.QuestionnaireID, user.ID)
	if err != nil {
		a.logger.Error("load questionnaire answers", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	members, err := a.questionnaireLoadMemberStatuses(c.Request.Context(), access.QuestionnaireID, access.TeamID)
	if err != nil {
		a.logger.Error("load questionnaire member statuses", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.HTML(http.StatusOK, "questionnaire.html", questionnairePageData{
		User:           user,
		CSRFToken:      csrfToken(c),
		JamID:          access.JamID,
		PublicJamID:    c.Param("id"),
		JamTitle:       access.JamTitle,
		Stage:          access.Stage,
		Writable:       access.Stage == StageUpcoming,
		ResponseStatus: status,
		Questions:      questions,
		Members:        members,
		Error:          c.Query("error"),
		Completed:      c.Query("completed") == "1",
	})
}

func (a *App) questionnaireAutosave(c *gin.Context) {
	jamID, ok := a.resolvePublicID(c, "id", "jams")
	if !ok {
		return
	}
	input, err := questionnaireParseAutosave(c)
	if err != nil {
		questionnaireJSONError(c, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.questionnaireInternalJSONError(c, "begin questionnaire autosave", err)
		return
	}
	defer tx.Rollback(c.Request.Context())

	user := CurrentUser(c)
	access, err := a.questionnaireLoadAccess(c.Request.Context(), tx, jamID, user.ID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		questionnaireJSONError(c, http.StatusNotFound, "Анкета не найдена.")
		return
	}
	if err != nil {
		a.questionnaireInternalJSONError(c, "load questionnaire autosave access", err)
		return
	}
	if access.Stage != StageUpcoming {
		questionnaireJSONError(c, http.StatusConflict, "Редактирование анкеты доступно только до начала приёма работ.")
		return
	}

	rule, optionIDs, err := questionnaireValidateAutosave(c.Request.Context(), tx, access.QuestionnaireID, input)
	if err != nil {
		var validationErr *questionnaireValidationError
		if errors.As(err, &validationErr) {
			questionnaireJSONError(c, http.StatusUnprocessableEntity, validationErr.Error())
		} else {
			a.questionnaireInternalJSONError(c, "validate questionnaire autosave", err)
		}
		return
	}

	responseID, status, err := questionnaireLockResponse(c.Request.Context(), tx, access.QuestionnaireID, access.TeamID, user.ID)
	if err != nil {
		a.questionnaireInternalJSONError(c, "lock questionnaire response", err)
		return
	}
	returnedToDraft := status == "completed"
	if returnedToDraft {
		snapshot, snapshotErr := questionnaireBuildSnapshot(c.Request.Context(), tx, access.QuestionnaireID, responseID)
		if snapshotErr != nil {
			a.questionnaireInternalJSONError(c, "snapshot completed questionnaire response", snapshotErr)
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `
			INSERT INTO questionnaire_response_history (response_id, event, snapshot)
			VALUES ($1, 'returned_to_draft', $2::jsonb)`, responseID, string(snapshot)); err != nil {
			a.questionnaireInternalJSONError(c, "record questionnaire draft history", err)
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `
			UPDATE questionnaire_responses
			SET status = 'draft', completed_at = NULL, updated_at = now()
			WHERE id = $1`, responseID); err != nil {
			a.questionnaireInternalJSONError(c, "return questionnaire response to draft", err)
			return
		}
	}

	if rule.Type == "short_text" {
		_, err = tx.Exec(c.Request.Context(), `
			INSERT INTO questionnaire_text_answers (response_id, question_id, value)
			VALUES ($1, $2, $3)
			ON CONFLICT (response_id, question_id) DO UPDATE
			SET value = EXCLUDED.value, updated_at = now()`, responseID, rule.ID, *input.Value)
	} else {
		if _, err = tx.Exec(c.Request.Context(), `
			DELETE FROM questionnaire_selected_options WHERE response_id = $1 AND question_id = $2`, responseID, rule.ID); err == nil {
			for _, optionID := range optionIDs {
				if _, err = tx.Exec(c.Request.Context(), `
					INSERT INTO questionnaire_selected_options (response_id, question_id, option_id)
					VALUES ($1, $2, $3)`, responseID, rule.ID, optionID); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		a.questionnaireInternalJSONError(c, "save questionnaire answer", err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE questionnaire_responses SET updated_at = now() WHERE id = $1`, responseID); err != nil {
		a.questionnaireInternalJSONError(c, "touch questionnaire response", err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.questionnaireInternalJSONError(c, "commit questionnaire autosave", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"saved": true, "status": "draft", "returned_to_draft": returnedToDraft})
}

func (a *App) questionnaireComplete(c *gin.Context) {
	jamID, ok := a.resolvePublicID(c, "id", "jams")
	if !ok {
		return
	}
	publicJamID := c.Param("id")
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.logger.Error("begin questionnaire completion", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(c.Request.Context())

	user := CurrentUser(c)
	access, err := a.questionnaireLoadAccess(c.Request.Context(), tx, jamID, user.ID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load questionnaire completion access", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if access.Stage != StageUpcoming {
		questionnaireRedirect(c, publicJamID, "error", "Завершить анкету можно только до начала приёма работ.")
		return
	}

	responseID, _, err := questionnaireLockResponse(c.Request.Context(), tx, access.QuestionnaireID, access.TeamID, user.ID)
	if err != nil {
		a.logger.Error("lock questionnaire response for completion", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	snapshot, err := questionnaireValidateCompletion(c.Request.Context(), tx, access.QuestionnaireID, responseID)
	if err != nil {
		var validationErr *questionnaireValidationError
		if errors.As(err, &validationErr) {
			questionnaireRedirect(c, publicJamID, "error", validationErr.Error())
		} else {
			a.logger.Error("validate questionnaire completion", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
		}
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `
		UPDATE questionnaire_responses
		SET status = 'completed', completed_at = now(), updated_at = now()
		WHERE id = $1`, responseID); err != nil {
		a.logger.Error("complete questionnaire response", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `
		INSERT INTO questionnaire_response_history (response_id, event, snapshot)
		VALUES ($1, 'completed', $2::jsonb)`, responseID, string(snapshot)); err != nil {
		a.logger.Error("record questionnaire completion", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.logger.Error("commit questionnaire completion", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	questionnaireRedirect(c, publicJamID, "completed", "1")
}

func questionnaireRedirect(c *gin.Context, publicJamID, key, value string) {
	location := fmt.Sprintf("/jams/%s/questionnaire?%s=%s", publicJamID, url.QueryEscape(key), url.QueryEscape(value))
	c.Redirect(http.StatusSeeOther, location)
}

type questionnaireQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (a *App) questionnaireLoadAccess(ctx context.Context, db questionnaireQueryRower, jamID, userID int64, lock bool) (questionnaireAccess, error) {
	query := `
		SELECT j.id, j.title, q.id, q.current_revision, t.id, j.submission_starts_at,
		       j.evaluation_starts_at, j.voting_starts_at, j.finishes_at, j.status_override
		FROM jams j
		JOIN questionnaires q ON q.jam_id = j.id
		JOIN team_members tm ON tm.jam_id = j.id AND tm.user_id = $2
		JOIN teams t ON t.id = tm.team_id AND t.jam_id = j.id
		WHERE j.id = $1 AND j.visibility = 'published'`
	if lock {
		query += ` FOR SHARE OF j, tm`
	}
	var access questionnaireAccess
	var schedule Schedule
	var override *string
	err := db.QueryRow(ctx, query, jamID, userID).Scan(
		&access.JamID, &access.JamTitle, &access.QuestionnaireID, &access.Revision, &access.TeamID,
		&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt,
		&schedule.FinishesAt, &override,
	)
	if err != nil {
		return questionnaireAccess{}, err
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	access.Stage = EffectiveStage(schedule, time.Now())
	return access, nil
}

func (a *App) questionnaireLoadOwnAnswers(ctx context.Context, questionnaireID, userID int64) ([]questionnaireQuestionView, string, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT question.id, question.type, question.prompt, question.hint, question.required,
		       question.text_limit, question.selection_limit
		FROM questionnaire_questions question
		JOIN questionnaires questionnaire ON questionnaire.id=question.questionnaire_id
		WHERE question.questionnaire_id = $1 AND question.revision=questionnaire.current_revision
		ORDER BY position, question.id`, questionnaireID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	questions := make([]questionnaireQuestionView, 0)
	questionIndexes := make(map[int64]int)
	for rows.Next() {
		var question questionnaireQuestionView
		var hint *string
		var textLimit, selectionLimit *int
		if err = rows.Scan(&question.ID, &question.Type, &question.Prompt, &hint, &question.Required, &textLimit, &selectionLimit); err != nil {
			return nil, "", err
		}
		if hint != nil {
			question.Hint = *hint
		}
		if textLimit != nil {
			question.TextLimit = *textLimit
		}
		if selectionLimit != nil {
			question.SelectionLimit = *selectionLimit
		}
		questionIndexes[question.ID] = len(questions)
		questions = append(questions, question)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()

	optionRows, err := a.pool.Query(ctx, `
		SELECT o.id, o.question_id, o.label
		FROM questionnaire_options o
		JOIN questionnaire_questions q ON q.id = o.question_id
		JOIN questionnaires questionnaire ON questionnaire.id=q.questionnaire_id
		WHERE q.questionnaire_id = $1 AND q.revision=questionnaire.current_revision
		ORDER BY q.position, o.position, o.id`, questionnaireID)
	if err != nil {
		return nil, "", err
	}
	defer optionRows.Close()
	for optionRows.Next() {
		var option questionnaireOptionView
		var questionID int64
		if err = optionRows.Scan(&option.ID, &questionID, &option.Label); err != nil {
			return nil, "", err
		}
		if index, exists := questionIndexes[questionID]; exists {
			questions[index].Options = append(questions[index].Options, option)
		}
	}
	if err = optionRows.Err(); err != nil {
		return nil, "", err
	}
	optionRows.Close()

	var responseID int64
	status := "draft"
	err = a.pool.QueryRow(ctx, `
		SELECT id, status FROM questionnaire_responses
		WHERE questionnaire_id = $1 AND revision=(SELECT current_revision FROM questionnaires WHERE id=$1)
		  AND user_id = $2`, questionnaireID, userID).Scan(&responseID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return questions, status, nil
	}
	if err != nil {
		return nil, "", err
	}
	textRows, err := a.pool.Query(ctx, `
		SELECT question_id, value FROM questionnaire_text_answers WHERE response_id = $1`, responseID)
	if err != nil {
		return nil, "", err
	}
	defer textRows.Close()
	for textRows.Next() {
		var questionID int64
		var value string
		if err = textRows.Scan(&questionID, &value); err != nil {
			return nil, "", err
		}
		if index, exists := questionIndexes[questionID]; exists {
			questions[index].TextValue = value
		}
	}
	if err = textRows.Err(); err != nil {
		return nil, "", err
	}
	textRows.Close()
	selectedRows, err := a.pool.Query(ctx, `
		SELECT question_id, option_id FROM questionnaire_selected_options WHERE response_id = $1`, responseID)
	if err != nil {
		return nil, "", err
	}
	defer selectedRows.Close()
	for selectedRows.Next() {
		var questionID, optionID int64
		if err = selectedRows.Scan(&questionID, &optionID); err != nil {
			return nil, "", err
		}
		if index, exists := questionIndexes[questionID]; exists {
			for optionIndex := range questions[index].Options {
				if questions[index].Options[optionIndex].ID == optionID {
					questions[index].Options[optionIndex].Selected = true
					break
				}
			}
		}
	}
	if err = selectedRows.Err(); err != nil {
		return nil, "", err
	}
	return questions, status, nil
}

func (a *App) questionnaireLoadMemberStatuses(ctx context.Context, questionnaireID, teamID int64) ([]questionnaireMemberView, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT u.username, COALESCE(r.status, 'draft')
		FROM team_members tm
		JOIN users u ON u.id = tm.user_id
		JOIN questionnaires questionnaire ON questionnaire.id=$1
		LEFT JOIN questionnaire_responses r ON r.questionnaire_id = $1
		  AND r.revision=questionnaire.current_revision AND r.user_id = tm.user_id
		WHERE tm.team_id = $2
		ORDER BY lower(u.username), u.id`, questionnaireID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]questionnaireMemberView, 0)
	for rows.Next() {
		var member questionnaireMemberView
		if err = rows.Scan(&member.Username, &member.Status); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func questionnaireParseAutosave(c *gin.Context) (questionnaireAutosaveInput, error) {
	var input questionnaireAutosaveInput
	if c.ContentType() == "application/json" {
		if err := c.ShouldBindJSON(&input); err != nil {
			return input, &questionnaireValidationError{message: "Некорректные данные ответа."}
		}
		return input, nil
	}
	if err := c.Request.ParseForm(); err != nil {
		return input, &questionnaireValidationError{message: "Некорректные данные ответа."}
	}
	questionID, err := strconv.ParseInt(c.Request.PostFormValue("question_id"), 10, 64)
	if err != nil {
		return input, &questionnaireValidationError{message: "Некорректный вопрос."}
	}
	input.QuestionID = questionID
	if values, exists := c.Request.PostForm["value"]; exists {
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		input.Value = &value
	}
	for _, rawID := range c.Request.PostForm["option_ids"] {
		optionID, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil {
			return input, &questionnaireValidationError{message: "Некорректный вариант ответа."}
		}
		input.OptionIDs = append(input.OptionIDs, optionID)
	}
	for _, rawID := range c.Request.PostForm["option_ids[]"] {
		optionID, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil {
			return input, &questionnaireValidationError{message: "Некорректный вариант ответа."}
		}
		input.OptionIDs = append(input.OptionIDs, optionID)
	}
	return input, nil
}

func questionnaireValidateAutosave(ctx context.Context, tx pgx.Tx, questionnaireID int64, input questionnaireAutosaveInput) (questionnaireQuestionRule, []int64, error) {
	var rule questionnaireQuestionRule
	var textLimit, selectionLimit *int
	err := tx.QueryRow(ctx, `
		SELECT id, type, required, text_limit, selection_limit
		FROM questionnaire_questions
		WHERE id = $1 AND questionnaire_id = $2
		  AND revision=(SELECT current_revision FROM questionnaires WHERE id=$2)
		FOR SHARE`, input.QuestionID, questionnaireID).Scan(&rule.ID, &rule.Type, &rule.Required, &textLimit, &selectionLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return rule, nil, &questionnaireValidationError{message: "Вопрос не относится к этой анкете."}
	}
	if err != nil {
		return rule, nil, err
	}
	if textLimit != nil {
		rule.TextLimit = *textLimit
	}
	if selectionLimit != nil {
		rule.SelectionLimit = *selectionLimit
	}
	if rule.Type == "short_text" {
		if input.Value == nil || len(input.OptionIDs) != 0 {
			return rule, nil, &questionnaireValidationError{message: "Для текстового вопроса ожидается текстовый ответ."}
		}
		if !utf8.ValidString(*input.Value) || utf8.RuneCountInString(*input.Value) > rule.TextLimit {
			return rule, nil, &questionnaireValidationError{message: fmt.Sprintf("Ответ не должен превышать %d символов.", rule.TextLimit)}
		}
		return rule, nil, nil
	}
	if input.Value != nil {
		return rule, nil, &questionnaireValidationError{message: "Для этого вопроса нужно выбрать вариант ответа."}
	}
	for _, optionID := range input.OptionIDs {
		if optionID <= 0 {
			return rule, nil, &questionnaireValidationError{message: "Некорректный вариант ответа."}
		}
	}
	optionIDs := questionnaireUniqueIDs(input.OptionIDs)
	if rule.Type == "single_choice" && len(optionIDs) > 1 {
		return rule, nil, &questionnaireValidationError{message: "Можно выбрать только один вариант."}
	}
	if rule.Type == "multiple_choice" && len(optionIDs) > rule.SelectionLimit {
		return rule, nil, &questionnaireValidationError{message: fmt.Sprintf("Можно выбрать не более %d вариантов.", rule.SelectionLimit)}
	}
	if len(optionIDs) == 0 {
		return rule, optionIDs, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM questionnaire_options WHERE question_id = $1 AND id = ANY($2) FOR SHARE`, rule.ID, optionIDs)
	if err != nil {
		return rule, nil, err
	}
	defer rows.Close()
	validCount := 0
	for rows.Next() {
		validCount++
	}
	if err = rows.Err(); err != nil {
		return rule, nil, err
	}
	if validCount != len(optionIDs) {
		return rule, nil, &questionnaireValidationError{message: "Выбран вариант, не относящийся к вопросу."}
	}
	return rule, optionIDs, nil
}

func questionnaireUniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func questionnaireLockResponse(ctx context.Context, tx pgx.Tx, questionnaireID, teamID, userID int64) (int64, string, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO questionnaire_responses (questionnaire_id, revision, team_id_at_start, user_id)
		SELECT id, current_revision, $2, $3 FROM questionnaires WHERE id=$1
		ON CONFLICT (questionnaire_id, revision, user_id) DO NOTHING`, questionnaireID, teamID, userID); err != nil {
		return 0, "", err
	}
	var responseID int64
	var status string
	err := tx.QueryRow(ctx, `
		SELECT id, status FROM questionnaire_responses
		WHERE questionnaire_id = $1 AND revision=(SELECT current_revision FROM questionnaires WHERE id=$1)
		  AND user_id = $2 FOR UPDATE`, questionnaireID, userID).Scan(&responseID, &status)
	return responseID, status, err
}

func questionnaireValidateCompletion(ctx context.Context, tx pgx.Tx, questionnaireID, responseID int64) ([]byte, error) {
	rules, textAnswers, selectedOptions, selectedLabels, err := questionnaireLoadSnapshotParts(ctx, tx, questionnaireID, responseID)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		text, hasText := textAnswers[rule.ID]
		options := selectedOptions[rule.ID]
		switch rule.Type {
		case "short_text":
			if len(options) != 0 {
				return nil, &questionnaireValidationError{message: "Один из ответов имеет неверный тип."}
			}
			if hasText && (!utf8.ValidString(text) || utf8.RuneCountInString(text) > rule.TextLimit) {
				return nil, &questionnaireValidationError{message: "Один из текстовых ответов превышает допустимую длину."}
			}
			if rule.Required && strings.TrimSpace(text) == "" {
				return nil, &questionnaireValidationError{message: "Ответьте на все обязательные вопросы."}
			}
		case "single_choice":
			if hasText || len(options) > 1 {
				return nil, &questionnaireValidationError{message: "Один из ответов имеет неверный тип."}
			}
			if rule.Required && len(options) == 0 {
				return nil, &questionnaireValidationError{message: "Ответьте на все обязательные вопросы."}
			}
		case "multiple_choice":
			if hasText || len(options) > rule.SelectionLimit {
				return nil, &questionnaireValidationError{message: "В одном из вопросов выбрано слишком много вариантов."}
			}
			if rule.Required && len(options) == 0 {
				return nil, &questionnaireValidationError{message: "Ответьте на все обязательные вопросы."}
			}
		default:
			return nil, fmt.Errorf("unknown questionnaire question type %q", rule.Type)
		}
	}
	return questionnaireMarshalSnapshot(rules, textAnswers, selectedOptions, selectedLabels)
}

func questionnaireBuildSnapshot(ctx context.Context, tx pgx.Tx, questionnaireID, responseID int64) ([]byte, error) {
	rules, textAnswers, selectedOptions, selectedLabels, err := questionnaireLoadSnapshotParts(ctx, tx, questionnaireID, responseID)
	if err != nil {
		return nil, err
	}
	return questionnaireMarshalSnapshot(rules, textAnswers, selectedOptions, selectedLabels)
}

func questionnaireLoadSnapshotParts(ctx context.Context, tx pgx.Tx, questionnaireID, responseID int64) ([]questionnaireQuestionRule, map[int64]string, map[int64][]int64, map[int64][]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT question.id, question.type, question.prompt, question.position,
		       question.required, question.text_limit, question.selection_limit
		FROM questionnaire_questions question
		JOIN questionnaires questionnaire ON questionnaire.id=question.questionnaire_id
		WHERE question.questionnaire_id = $1 AND question.revision=questionnaire.current_revision
		ORDER BY position, question.id FOR SHARE OF question`, questionnaireID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rules := make([]questionnaireQuestionRule, 0)
	knownQuestions := make(map[int64]questionnaireQuestionRule)
	for rows.Next() {
		var rule questionnaireQuestionRule
		var textLimit, selectionLimit *int
		if err = rows.Scan(&rule.ID, &rule.Type, &rule.Prompt, &rule.Position, &rule.Required, &textLimit, &selectionLimit); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		if textLimit != nil {
			rule.TextLimit = *textLimit
		}
		if selectionLimit != nil {
			rule.SelectionLimit = *selectionLimit
		}
		rules = append(rules, rule)
		knownQuestions[rule.ID] = rule
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nil, nil, nil, err
	}
	rows.Close()

	textAnswers := make(map[int64]string)
	textRows, err := tx.Query(ctx, `SELECT question_id, value FROM questionnaire_text_answers WHERE response_id = $1`, responseID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for textRows.Next() {
		var questionID int64
		var value string
		if err = textRows.Scan(&questionID, &value); err != nil {
			textRows.Close()
			return nil, nil, nil, nil, err
		}
		if _, exists := knownQuestions[questionID]; exists {
			textAnswers[questionID] = value
		}
	}
	if err = textRows.Err(); err != nil {
		textRows.Close()
		return nil, nil, nil, nil, err
	}
	textRows.Close()

	selectedOptions := make(map[int64][]int64)
	selectedLabels := make(map[int64][]string)
	optionRows, err := tx.Query(ctx, `
		SELECT s.question_id, s.option_id, o.question_id, o.label
		FROM questionnaire_selected_options s
		JOIN questionnaire_options o ON o.id = s.option_id
		WHERE s.response_id = $1 ORDER BY s.question_id, o.position, s.option_id`, responseID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for optionRows.Next() {
		var questionID, optionID, ownerQuestionID int64
		var label string
		if err = optionRows.Scan(&questionID, &optionID, &ownerQuestionID, &label); err != nil {
			optionRows.Close()
			return nil, nil, nil, nil, err
		}
		if _, exists := knownQuestions[questionID]; !exists || ownerQuestionID != questionID {
			optionRows.Close()
			return nil, nil, nil, nil, &questionnaireValidationError{message: "Один из выбранных вариантов больше не относится к вопросу."}
		}
		selectedOptions[questionID] = append(selectedOptions[questionID], optionID)
		selectedLabels[questionID] = append(selectedLabels[questionID], label)
	}
	if err = optionRows.Err(); err != nil {
		optionRows.Close()
		return nil, nil, nil, nil, err
	}
	optionRows.Close()
	return rules, textAnswers, selectedOptions, selectedLabels, nil
}

func questionnaireMarshalSnapshot(rules []questionnaireQuestionRule, textAnswers map[int64]string, selectedOptions map[int64][]int64, selectedLabels map[int64][]string) ([]byte, error) {
	snapshot := questionnaireSnapshot{Answers: make([]questionnaireSnapshotAnswer, 0, len(rules))}
	for _, rule := range rules {
		answer := questionnaireSnapshotAnswer{QuestionID: rule.ID, Type: rule.Type, Prompt: rule.Prompt, Position: rule.Position}
		if text, exists := textAnswers[rule.ID]; exists {
			answer.Text = &text
		}
		if options := selectedOptions[rule.ID]; len(options) > 0 {
			answer.OptionIDs = options
			answer.OptionLabels = selectedLabels[rule.ID]
		}
		snapshot.Answers = append(snapshot.Answers, answer)
	}
	return json.Marshal(snapshot)
}

func questionnaireJSONError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

func (a *App) questionnaireInternalJSONError(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	questionnaireJSONError(c, http.StatusInternalServerError, "Не удалось сохранить ответ. Попробуйте позже.")
}

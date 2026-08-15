package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type adminQuestionnaireTeamReport struct {
	ID                  int64
	Name                string
	CurrentMembers      int
	NotStarted          int
	Draft               int
	Completed           int
	FormerRespondents   int
	AutomaticEligible   bool
	OverrideSet         bool
	OverrideAllowed     bool
	EffectivelyEligible bool
}

type adminQuestionnaireRespondent struct {
	UserID          int64
	Username        string
	ResponseID      int64
	Revision        int
	CurrentRevision bool
	Status          string
	TeamAtStartID   int64
	TeamAtStartName string
	CurrentTeamID   int64
	CurrentTeamName string
	CreatedAt       *time.Time
	UpdatedAt       *time.Time
	CompletedAt     *time.Time
}

type adminQuestionnaireReportsData struct {
	PageData
	Jam              *adminJam
	CurrentRevision  int
	SelectedTeamID   int64
	SelectedTeamName string
	Teams            []adminQuestionnaireTeamReport
	Respondents      []adminQuestionnaireRespondent
}

type adminQuestionnaireAnswer struct {
	QuestionID int64
	Position   int
	Type       string
	Prompt     string
	Required   bool
	Answer     string
}

type adminQuestionnaireHistory struct {
	Event     string
	CreatedAt time.Time
	Answers   []adminQuestionnaireAnswer
}

type adminQuestionnaireResponseData struct {
	PageData
	Jam      *adminJam
	Response adminQuestionnaireRespondent
	Answers  []adminQuestionnaireAnswer
	History  []adminQuestionnaireHistory
}

func (a *App) questionnaireAdminReportsPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	selectedTeamID, valid := adminQuestionnaireTeamFilter(c)
	if !valid {
		a.writeError(c, http.StatusBadRequest, "Некорректный фильтр команды.")
		return
	}
	tx, err := a.pool.BeginTx(c.Request.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		a.logger.Error("begin questionnaire admin report", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(c.Request.Context())
	data, err := a.loadQuestionnaireAdminReports(c.Request.Context(), tx, jamID, selectedTeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load questionnaire admin reports", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.logger.Error("commit questionnaire admin report", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	data.User, data.CSRFToken = CurrentUser(c), csrfToken(c)
	c.HTML(http.StatusOK, "admin_questionnaire_reports.html", data)
}

func (a *App) questionnaireAdminResponsePage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	responseID, ok := adminID(c, "responseID")
	if !ok {
		return
	}
	tx, err := a.pool.BeginTx(c.Request.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		a.logger.Error("begin questionnaire response report", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(c.Request.Context())
	data, err := a.loadQuestionnaireAdminResponse(c.Request.Context(), tx, jamID, responseID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load questionnaire admin response", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.logger.Error("commit questionnaire response report", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	data.User, data.CSRFToken = CurrentUser(c), csrfToken(c)
	c.HTML(http.StatusOK, "admin_questionnaire_response.html", data)
}

func (a *App) questionnaireAdminReportsCSV(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	selectedTeamID, valid := adminQuestionnaireTeamFilter(c)
	if !valid {
		a.writeError(c, http.StatusBadRequest, "Некорректный фильтр команды.")
		return
	}
	tx, err := a.pool.BeginTx(c.Request.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		a.questionnaireCSVFailure(c, err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	reports, err := a.loadQuestionnaireAdminReports(c.Request.Context(), tx, jamID, selectedTeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load questionnaire CSV report", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	var buffer bytes.Buffer
	buffer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true
	header := []string{"record_kind", "response_id", "revision", "user_id", "username", "team_at_start_id", "team_at_start", "current_team_id", "current_team", "response_status", "history_event", "event_at", "question_id", "question_position", "question_type", "question", "answer"}
	if err = writer.Write(header); err != nil {
		a.questionnaireCSVFailure(c, err)
		return
	}
	for _, respondent := range reports.Respondents {
		if respondent.ResponseID == 0 {
			row := questionnaireCSVBaseRow("not_started", respondent)
			if err = writer.Write(questionnaireCSVCells(append(row, "", "", "", "", "", "", ""))); err != nil {
				a.questionnaireCSVFailure(c, err)
				return
			}
			continue
		}
		response, loadErr := a.loadQuestionnaireAdminResponse(c.Request.Context(), tx, jamID, respondent.ResponseID)
		if loadErr != nil {
			a.questionnaireCSVFailure(c, loadErr)
			return
		}
		for _, answer := range response.Answers {
			row := append(questionnaireCSVBaseRow("current", respondent), "", formatOptionalTime(respondent.UpdatedAt), strconv.FormatInt(answer.QuestionID, 10), strconv.Itoa(answer.Position), answer.Type, answer.Prompt, answer.Answer)
			if err = writer.Write(questionnaireCSVCells(row)); err != nil {
				a.questionnaireCSVFailure(c, err)
				return
			}
		}
		for _, history := range response.History {
			for _, answer := range history.Answers {
				row := append(questionnaireCSVBaseRow("history", respondent), history.Event, history.CreatedAt.UTC().Format(time.RFC3339), strconv.FormatInt(answer.QuestionID, 10), strconv.Itoa(answer.Position), answer.Type, answer.Prompt, answer.Answer)
				if err = writer.Write(questionnaireCSVCells(row)); err != nil {
					a.questionnaireCSVFailure(c, err)
					return
				}
			}
		}
	}
	writer.Flush()
	if err = writer.Error(); err != nil {
		a.questionnaireCSVFailure(c, err)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		a.questionnaireCSVFailure(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="questionnaire-jam-%d.csv"`, jamID))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

func (a *App) loadQuestionnaireAdminReports(ctx context.Context, db queryDB, jamID, selectedTeamID int64) (adminQuestionnaireReportsData, error) {
	data := adminQuestionnaireReportsData{}
	jam, err := scanAdminJam(db.QueryRow(ctx, adminJamSQL(false), jamID))
	if err != nil {
		return data, err
	}
	data.Jam = jam
	var questionnaireID int64
	if err = db.QueryRow(ctx, `SELECT id, current_revision FROM questionnaires WHERE jam_id=$1`, jamID).Scan(&questionnaireID, &data.CurrentRevision); err != nil {
		return data, err
	}
	if selectedTeamID != 0 {
		if err = db.QueryRow(ctx, `SELECT name FROM teams WHERE id=$1 AND jam_id=$2`, selectedTeamID, jamID).Scan(&data.SelectedTeamName); err != nil {
			return data, err
		}
		data.SelectedTeamID = selectedTeamID
	}
	teamRows, err := db.Query(ctx, `
		SELECT team.id, team.name, count(member.user_id),
		       count(member.user_id) FILTER (WHERE response.id IS NULL),
		       count(response.id) FILTER (WHERE response.status='draft'),
		       count(response.id) FILTER (WHERE response.status='completed'),
		       (SELECT count(*) FROM questionnaire_responses former
		        WHERE former.questionnaire_id=$2 AND former.revision=$3 AND former.team_id_at_start=team.id
		          AND NOT EXISTS (SELECT 1 FROM team_members current_member
		                          WHERE current_member.team_id=team.id AND current_member.user_id=former.user_id)),
		       COALESCE(bool_or(response.status='completed'), false), override.allowed
		FROM teams team
		LEFT JOIN team_members member ON member.team_id=team.id
		LEFT JOIN questionnaire_responses response ON response.questionnaire_id=$2
		  AND response.revision=$3 AND response.user_id=member.user_id
		LEFT JOIN team_eligibility_overrides override ON override.team_id=team.id
		WHERE team.jam_id=$1
		GROUP BY team.id, override.allowed ORDER BY lower(team.name), team.id`, jamID, questionnaireID, data.CurrentRevision)
	if err != nil {
		return data, err
	}
	for teamRows.Next() {
		var team adminQuestionnaireTeamReport
		var override *bool
		if err = teamRows.Scan(&team.ID, &team.Name, &team.CurrentMembers, &team.NotStarted, &team.Draft, &team.Completed, &team.FormerRespondents, &team.AutomaticEligible, &override); err != nil {
			teamRows.Close()
			return data, err
		}
		if override != nil {
			team.OverrideSet, team.OverrideAllowed = true, *override
		}
		team.EffectivelyEligible = team.AutomaticEligible || team.OverrideSet && team.OverrideAllowed
		data.Teams = append(data.Teams, team)
	}
	if err = teamRows.Err(); err != nil {
		teamRows.Close()
		return data, err
	}
	teamRows.Close()
	rows, err := db.Query(ctx, `
		WITH candidates AS (
			SELECT response.user_id, response.id AS response_id, response.revision
			FROM questionnaire_responses response WHERE response.questionnaire_id=$2
			UNION ALL
			SELECT member.user_id, NULL::bigint, $3
			FROM team_members member
			WHERE member.jam_id=$1 AND NOT EXISTS (
				SELECT 1 FROM questionnaire_responses response
				WHERE response.questionnaire_id=$2 AND response.revision=$3 AND response.user_id=member.user_id)
		)
		SELECT user_account.id, user_account.username, COALESCE(response.id, 0), candidates.revision,
		       candidates.revision=$3, COALESCE(response.status, 'not_started'),
		       COALESCE(start_team.id, 0), COALESCE(start_team.name, ''),
		       COALESCE(current_team.id, 0), COALESCE(current_team.name, ''),
		       response.created_at, response.updated_at, response.completed_at
		FROM candidates
		JOIN users user_account ON user_account.id=candidates.user_id
		LEFT JOIN questionnaire_responses response ON response.id=candidates.response_id
		LEFT JOIN teams start_team ON start_team.id=response.team_id_at_start AND start_team.jam_id=$1
		LEFT JOIN team_members current_member ON current_member.jam_id=$1 AND current_member.user_id=user_account.id
		LEFT JOIN teams current_team ON current_team.id=current_member.team_id
		WHERE $4::bigint=0 OR start_team.id=$4 OR current_team.id=$4
		ORDER BY candidates.revision DESC, lower(user_account.username), user_account.id, response.id`, jamID, questionnaireID, data.CurrentRevision, selectedTeamID)
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var respondent adminQuestionnaireRespondent
		if err = rows.Scan(&respondent.UserID, &respondent.Username, &respondent.ResponseID, &respondent.Revision, &respondent.CurrentRevision, &respondent.Status,
			&respondent.TeamAtStartID, &respondent.TeamAtStartName, &respondent.CurrentTeamID, &respondent.CurrentTeamName,
			&respondent.CreatedAt, &respondent.UpdatedAt, &respondent.CompletedAt); err != nil {
			return data, err
		}
		data.Respondents = append(data.Respondents, respondent)
	}
	return data, rows.Err()
}

func (a *App) loadQuestionnaireAdminResponse(ctx context.Context, db queryDB, jamID, responseID int64) (adminQuestionnaireResponseData, error) {
	data := adminQuestionnaireResponseData{}
	jam, err := scanAdminJam(db.QueryRow(ctx, adminJamSQL(false), jamID))
	if err != nil {
		return data, err
	}
	data.Jam = jam
	err = db.QueryRow(ctx, `
		SELECT user_account.id, user_account.username, response.id, response.revision,
		       response.revision=questionnaire.current_revision, response.status,
		       start_team.id, start_team.name, COALESCE(current_team.id, 0), COALESCE(current_team.name, ''),
		       response.created_at, response.updated_at, response.completed_at
		FROM questionnaire_responses response
		JOIN questionnaires questionnaire ON questionnaire.id=response.questionnaire_id AND questionnaire.jam_id=$1
		JOIN users user_account ON user_account.id=response.user_id
		JOIN teams start_team ON start_team.id=response.team_id_at_start AND start_team.jam_id=$1
		LEFT JOIN team_members current_member ON current_member.jam_id=$1 AND current_member.user_id=response.user_id
		LEFT JOIN teams current_team ON current_team.id=current_member.team_id
		WHERE response.id=$2`, jamID, responseID).Scan(
		&data.Response.UserID, &data.Response.Username, &data.Response.ResponseID, &data.Response.Revision,
		&data.Response.CurrentRevision, &data.Response.Status, &data.Response.TeamAtStartID,
		&data.Response.TeamAtStartName, &data.Response.CurrentTeamID, &data.Response.CurrentTeamName,
		&data.Response.CreatedAt, &data.Response.UpdatedAt, &data.Response.CompletedAt)
	if err != nil {
		return data, err
	}
	rows, err := db.Query(ctx, `
		SELECT question.id, question.position, question.type, question.prompt, question.required,
		       COALESCE(text_answer.value, ''),
		       COALESCE(array_agg(option.label ORDER BY option.position, option.id)
		         FILTER (WHERE option.id IS NOT NULL), ARRAY[]::varchar[])
		FROM questionnaire_responses response
		JOIN questionnaire_questions question ON question.questionnaire_id=response.questionnaire_id
		  AND question.revision=response.revision
		LEFT JOIN questionnaire_text_answers text_answer ON text_answer.response_id=response.id AND text_answer.question_id=question.id
		LEFT JOIN questionnaire_selected_options selected ON selected.response_id=response.id AND selected.question_id=question.id
		LEFT JOIN questionnaire_options option ON option.id=selected.option_id AND option.question_id=selected.question_id
		WHERE response.id=$1
		GROUP BY question.id, text_answer.value ORDER BY question.position, question.id`, responseID)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var answer adminQuestionnaireAnswer
		var textValue string
		var optionLabels []string
		if err = rows.Scan(&answer.QuestionID, &answer.Position, &answer.Type, &answer.Prompt, &answer.Required, &textValue, &optionLabels); err != nil {
			rows.Close()
			return data, err
		}
		answer.Answer = textValue
		if answer.Type != "short_text" {
			answer.Answer = strings.Join(optionLabels, "\n")
		}
		data.Answers = append(data.Answers, answer)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	historyRows, err := db.Query(ctx, `
		SELECT history.event, history.snapshot, history.created_at
		FROM questionnaire_response_history history WHERE history.response_id=$1
		ORDER BY history.created_at DESC, history.id DESC`, responseID)
	if err != nil {
		return data, err
	}
	defer historyRows.Close()
	for historyRows.Next() {
		var history adminQuestionnaireHistory
		var raw []byte
		if err = historyRows.Scan(&history.Event, &raw, &history.CreatedAt); err != nil {
			return data, err
		}
		var snapshot questionnaireSnapshot
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return data, err
		}
		for _, item := range snapshot.Answers {
			prompt := item.Prompt
			if prompt == "" {
				prompt = fmt.Sprintf("Вопрос #%d", item.QuestionID)
			}
			answer := adminQuestionnaireAnswer{QuestionID: item.QuestionID, Position: item.Position, Type: item.Type, Prompt: prompt}
			if item.Text != nil {
				answer.Answer = *item.Text
			} else if len(item.OptionLabels) > 0 {
				answer.Answer = strings.Join(item.OptionLabels, "\n")
			} else if len(item.OptionIDs) > 0 {
				parts := make([]string, len(item.OptionIDs))
				for index, optionID := range item.OptionIDs {
					parts[index] = fmt.Sprintf("Вариант #%d", optionID)
				}
				answer.Answer = strings.Join(parts, "\n")
			}
			history.Answers = append(history.Answers, answer)
		}
		data.History = append(data.History, history)
	}
	return data, historyRows.Err()
}

func adminQuestionnaireTeamFilter(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Query("team_id"))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func questionnaireCSVCell(value string) string {
	candidate := strings.TrimLeftFunc(value, unicode.IsSpace)
	if candidate == "" {
		return value
	}
	first, _ := utf8.DecodeRuneInString(candidate)
	if first == '=' || first == '+' || first == '-' || first == '@' {
		return "'" + value
	}
	return value
}

func questionnaireCSVCells(values []string) []string {
	for index := range values {
		values[index] = questionnaireCSVCell(values[index])
	}
	return values
}

func questionnaireCSVBaseRow(kind string, respondent adminQuestionnaireRespondent) []string {
	return []string{kind, optionalID(respondent.ResponseID), strconv.Itoa(respondent.Revision), strconv.FormatInt(respondent.UserID, 10), respondent.Username,
		optionalID(respondent.TeamAtStartID), respondent.TeamAtStartName, optionalID(respondent.CurrentTeamID), respondent.CurrentTeamName, respondent.Status}
}

func optionalID(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func (a *App) questionnaireCSVFailure(c *gin.Context, err error) {
	a.logger.Error("build questionnaire CSV", "error", err)
	c.AbortWithStatus(http.StatusInternalServerError)
}

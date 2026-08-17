package web

import (
	"context"
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
	"github.com/jackc/pgx/v5/pgconn"
)

type ThemeView struct {
	ID     int64
	Phrase string
}

type adminTheme struct {
	ID                int64
	Phrase            string
	CopiedFromThemeID int64
	Withdrawn         bool
	SelectedCount     int
}

type adminThemesPageData struct {
	PageData
	Jam            *adminJam
	Themes         []adminTheme
	TeamSelections []adminTeamThemeSelection
}

type adminTeamThemeSelection struct {
	TeamID      int64
	TeamName    string
	ThemeID     int64
	ThemePhrase string
}

func canDiscloseTeamTheme(stage Stage, isMember bool) bool {
	return StageAtLeast(stage, StageEvaluation) || (stage == StageSubmission && isMember)
}

func (a *App) registerThemeRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("/jams/:id/themes", a.adminThemesPage)
	admin.POST("/jams/:id/themes", a.adminThemeCreate)
	admin.POST("/jams/:id/themes/:themeID/edit", a.adminThemeEdit)
	admin.POST("/jams/:id/themes/:themeID/withdraw", a.adminThemeWithdraw)
	admin.POST("/jams/:id/themes/copy", a.adminThemeCopy)
	admin.POST("/jams/:id/themes/team-selection", a.adminTeamThemeSelect)

	router.POST("/teams/:id/theme", RequireAuth(), a.teamThemeSelect)
}

func (a *App) adminThemesPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	a.renderAdminThemes(c, jamID, c.Query("error"), http.StatusOK)
}

func (a *App) adminThemeCreate(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	phrase, err := validateThemePhrase(c.PostForm("phrase"))
	if err != nil {
		a.renderAdminThemes(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.adminThemeInsert(c, jamID, phrase, 0)
}

func (a *App) adminThemeCopy(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	sourceID, ok := positiveFormID(c.PostForm("source_theme_id"))
	if !ok {
		a.renderAdminThemes(c, jamID, "Укажите исходную тему.", http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.themeAdminFailure(c, "begin theme copy", err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockThemeJam(ctx, tx, jamID); err != nil {
		a.handleThemeAdminLoadError(c, "lock target jam for theme copy", err)
		return
	}
	var phrase string
	if err = tx.QueryRow(ctx, `SELECT phrase FROM jam_themes WHERE id=$1 FOR SHARE`, sourceID).Scan(&phrase); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.renderAdminThemes(c, jamID, "Исходная тема не найдена.", http.StatusUnprocessableEntity)
			return
		}
		a.themeAdminFailure(c, "load source theme", err)
		return
	}
	if err = a.insertThemeTx(c, tx, jamID, phrase, sourceID); err != nil {
		a.handleThemeMutationError(c, jamID, "copy theme", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeAdminFailure(c, "commit theme copy", err)
		return
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/themes", jamID), "Тема скопирована независимой записью.")
}

func (a *App) adminThemeInsert(c *gin.Context, jamID int64, phrase string, copiedFromID int64) {
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.themeAdminFailure(c, "begin theme creation", err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockThemeJam(ctx, tx, jamID); err != nil {
		a.handleThemeAdminLoadError(c, "lock jam for theme creation", err)
		return
	}
	if err = a.insertThemeTx(c, tx, jamID, phrase, copiedFromID); err != nil {
		a.handleThemeMutationError(c, jamID, "create theme", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeAdminFailure(c, "commit theme creation", err)
		return
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/themes", jamID), "Тема добавлена.")
}

func (a *App) insertThemeTx(c *gin.Context, tx pgx.Tx, jamID int64, phrase string, copiedFromID int64) error {
	var copiedFrom any
	if copiedFromID > 0 {
		copiedFrom = copiedFromID
	}
	if _, err := tx.Exec(c.Request.Context(), `
		INSERT INTO jam_themes (jam_id, phrase, copied_from_theme_id)
		VALUES ($1, $2, $3)`, jamID, phrase, copiedFrom); err != nil {
		return err
	}
	return nil
}

func (a *App) adminThemeEdit(c *gin.Context) {
	jamID, themeID, ok := adminThemeIDs(c)
	if !ok {
		return
	}
	phrase, err := validateThemePhrase(c.PostForm("phrase"))
	if err != nil {
		a.renderAdminThemes(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.themeAdminFailure(c, "begin theme edit", err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockThemeJam(ctx, tx, jamID); err != nil {
		a.handleThemeAdminLoadError(c, "lock jam for theme edit", err)
		return
	}
	var beforePhrase string
	var withdrawn bool
	err = tx.QueryRow(ctx, `SELECT phrase, withdrawn_at IS NOT NULL FROM jam_themes WHERE id=$1 AND jam_id=$2 FOR UPDATE`, themeID, jamID).Scan(&beforePhrase, &withdrawn)
	if err != nil {
		a.handleThemeAdminLoadError(c, "lock theme for edit", err)
		return
	}
	if withdrawn {
		a.renderAdminThemes(c, jamID, "Отозванную тему нельзя редактировать: её история должна оставаться неизменной.", http.StatusConflict)
		return
	}
	if beforePhrase == phrase {
		a.renderAdminThemes(c, jamID, "Формулировка темы не изменилась.", http.StatusConflict)
		return
	}
	var selected bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_theme_selections WHERE theme_id=$1)`, themeID).Scan(&selected); err != nil {
		a.themeAdminFailure(c, "check selected theme before edit", err)
		return
	}
	if selected {
		a.renderAdminThemes(c, jamID, "Тема выбрана командой. Сначала переназначьте выбор команды, чтобы не изменить исторический смысл.", http.StatusConflict)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE jam_themes SET phrase=$3, updated_at=now() WHERE id=$1 AND jam_id=$2`, themeID, jamID, phrase); err != nil {
		a.handleThemeMutationError(c, jamID, "edit theme", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeAdminFailure(c, "commit theme edit", err)
		return
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/themes", jamID), "Формулировка темы обновлена.")
}

func (a *App) adminThemeWithdraw(c *gin.Context) {
	jamID, themeID, ok := adminThemeIDs(c)
	if !ok {
		return
	}
	if c.PostForm("confirm") != "withdraw" {
		a.renderAdminThemes(c, jamID, "Подтвердите отзыв темы.", http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.themeAdminFailure(c, "begin theme withdrawal", err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockThemeJam(ctx, tx, jamID); err != nil {
		a.handleThemeAdminLoadError(c, "lock jam for theme withdrawal", err)
		return
	}
	var withdrawn bool
	err = tx.QueryRow(ctx, `SELECT withdrawn_at IS NOT NULL FROM jam_themes WHERE id=$1 AND jam_id=$2 FOR UPDATE`, themeID, jamID).Scan(&withdrawn)
	if err != nil {
		a.handleThemeAdminLoadError(c, "lock theme for withdrawal", err)
		return
	}
	if withdrawn {
		a.renderAdminThemes(c, jamID, "Тема уже отозвана.", http.StatusConflict)
		return
	}
	var selected bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_theme_selections WHERE theme_id=$1)`, themeID).Scan(&selected); err != nil {
		a.themeAdminFailure(c, "check theme selections", err)
		return
	}
	if selected {
		a.renderAdminThemes(c, jamID, "Тема выбрана командой. Сначала выполните явное переназначение; автоматическая замена запрещена.", http.StatusConflict)
		return
	}
	stage, err := loadThemeJamStage(ctx, tx, jamID)
	if err != nil {
		a.themeAdminFailure(c, "load stage before theme withdrawal", err)
		return
	}
	if StageAtLeast(stage, StageSubmission) {
		var remaining int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM jam_themes WHERE jam_id=$1 AND withdrawn_at IS NULL AND id<>$2`, jamID, themeID).Scan(&remaining); err != nil {
			a.themeAdminFailure(c, "count themes before withdrawal", err)
			return
		}
		if remaining == 0 {
			a.renderAdminThemes(c, jamID, "Нельзя отозвать последнюю активную тему после начала submission.", http.StatusConflict)
			return
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE jam_themes SET withdrawn_at=now(), updated_at=now() WHERE id=$1`, themeID); err != nil {
		a.themeAdminFailure(c, "withdraw theme", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeAdminFailure(c, "commit theme withdrawal", err)
		return
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/themes", jamID), "Тема отозвана, историческая запись сохранена.")
}

func (a *App) adminTeamThemeSelect(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	teamID, ok := positiveFormID(c.PostForm("team_id"))
	if !ok {
		a.renderAdminThemes(c, jamID, "Укажите команду.", http.StatusUnprocessableEntity)
		return
	}
	themeID, ok := positiveFormID(c.PostForm("theme_id"))
	if !ok {
		a.renderAdminThemes(c, jamID, "Укажите новую тему.", http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.themeAdminFailure(c, "begin admin team theme selection", err)
		return
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `
		SELECT 1 FROM teams team JOIN jams jam ON jam.id=team.jam_id
		WHERE team.id=$1 AND team.jam_id=$2 FOR UPDATE OF team FOR SHARE OF jam`, teamID, jamID).Scan(new(int)); err != nil {
		a.handleThemeAdminLoadError(c, "lock team for theme intervention", err)
		return
	}
	if err = tx.QueryRow(ctx, `SELECT 1 FROM jam_themes WHERE id=$1 AND jam_id=$2 AND withdrawn_at IS NULL FOR SHARE`, themeID, jamID).Scan(new(int)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.renderAdminThemes(c, jamID, "Активная тема не найдена в этом джеме.", http.StatusUnprocessableEntity)
			return
		}
		a.themeAdminFailure(c, "load intervention theme", err)
		return
	}
	var oldThemeID int64
	err = tx.QueryRow(ctx, `
		SELECT s.theme_id FROM team_theme_selections s
		WHERE s.team_id=$1 FOR UPDATE OF s`, teamID).Scan(&oldThemeID)
	if err == nil && oldThemeID == themeID {
		a.renderAdminThemes(c, jamID, "У команды уже выбрана эта тема.", http.StatusConflict)
		return
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.themeAdminFailure(c, "load previous team theme", err)
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO team_theme_selections (team_id, jam_id, theme_id, selected_by_user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id) DO UPDATE SET theme_id=EXCLUDED.theme_id,
		selected_by_user_id=EXCLUDED.selected_by_user_id, updated_at=now()`, teamID, jamID, themeID, CurrentUser(c).ID); err != nil {
		a.themeAdminFailure(c, "set team theme as admin", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeAdminFailure(c, "commit team theme intervention", err)
		return
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/themes", jamID), "Тема команды переназначена.")
}

func (a *App) teamThemeSelect(c *gin.Context) {
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	publicTeamID := c.Param("id")
	themeID, ok := positiveFormID(c.PostForm("theme_id"))
	if !ok {
		themeSelectionRedirectError(c, publicTeamID, "Выберите тему.")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin theme selection", "error", err)
		themeSelectionRedirectError(c, publicTeamID, "Не удалось выбрать тему. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil {
		// Do not distinguish hidden teams or draft jams from unavailable selection.
		themeSelectionRedirectError(c, publicTeamID, "Выбор темы сейчас недоступен.")
		return
	}
	user := CurrentUser(c)
	if team.CaptainID != user.ID || teamEffectiveStage(team) != StageSubmission {
		themeSelectionRedirectError(c, publicTeamID, "Выбор темы сейчас недоступен.")
		return
	}
	var member, eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id=$1 AND jam_id=$2 AND user_id=$3)`, teamID, team.JamID, user.ID).Scan(&member); err != nil {
		a.themeSelectionFailure(c, publicTeamID, err)
		return
	}
	if !member {
		themeSelectionRedirectError(c, publicTeamID, "Выбор темы сейчас недоступен.")
		return
	}
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_members member
			JOIN questionnaires q ON q.jam_id=member.jam_id
			JOIN questionnaire_responses r ON r.questionnaire_id=q.id AND r.revision=q.current_revision
			  AND r.user_id=member.user_id AND r.status='completed'
			WHERE member.team_id=$1
		) OR COALESCE((SELECT allowed FROM team_eligibility_overrides WHERE team_id=$1), false)`, teamID).Scan(&eligible); err != nil {
		a.themeSelectionFailure(c, publicTeamID, err)
		return
	}
	if !eligible {
		themeSelectionRedirectError(c, publicTeamID, "Для выбора темы команда должна получить допуск.")
		return
	}
	var lockedThemeID int64
	var lockedPhrase string
	if err = tx.QueryRow(ctx, `SELECT id, phrase FROM jam_themes WHERE id=$1 AND jam_id=$2 AND withdrawn_at IS NULL FOR SHARE`, themeID, team.JamID).Scan(&lockedThemeID, &lockedPhrase); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			themeSelectionRedirectError(c, publicTeamID, "Выбранная тема недоступна.")
			return
		}
		a.themeSelectionFailure(c, publicTeamID, err)
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO team_theme_selections (team_id, jam_id, theme_id, selected_by_user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id) DO UPDATE SET theme_id=EXCLUDED.theme_id,
			selected_by_user_id=EXCLUDED.selected_by_user_id, updated_at=now()`, teamID, team.JamID, themeID, user.ID)
	if err != nil {
		a.themeSelectionFailure(c, publicTeamID, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.themeSelectionFailure(c, publicTeamID, err)
		return
	}
	c.Redirect(http.StatusSeeOther, "/?ok="+url.QueryEscape("Тема команды отмечена печатью: «"+lockedPhrase+"»")+"#jam")
}

func (a *App) renderAdminThemes(c *gin.Context, jamID int64, message string, status int) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load jam themes", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT t.id, t.phrase, COALESCE(t.copied_from_theme_id, 0), t.withdrawn_at IS NOT NULL,
		       count(s.team_id)
		FROM jam_themes t LEFT JOIN team_theme_selections s ON s.theme_id=t.id
		WHERE t.jam_id=$1 GROUP BY t.id ORDER BY t.withdrawn_at NULLS FIRST, t.created_at, t.id`, jamID)
	if err != nil {
		a.themeAdminFailure(c, "load themes", err)
		return
	}
	defer rows.Close()
	var themes []adminTheme
	for rows.Next() {
		var theme adminTheme
		if err = rows.Scan(&theme.ID, &theme.Phrase, &theme.CopiedFromThemeID, &theme.Withdrawn, &theme.SelectedCount); err != nil {
			a.themeAdminFailure(c, "scan themes", err)
			return
		}
		themes = append(themes, theme)
	}
	if err = rows.Err(); err != nil {
		a.themeAdminFailure(c, "iterate themes", err)
		return
	}
	selectionRows, err := a.pool.Query(c.Request.Context(), `
		SELECT team.id, team.name, COALESCE(selection.theme_id, 0), COALESCE(theme.phrase, '')
		FROM teams team
		LEFT JOIN team_theme_selections selection ON selection.team_id=team.id
		LEFT JOIN jam_themes theme ON theme.id=selection.theme_id
		WHERE team.jam_id=$1 ORDER BY lower(team.name), team.id`, jamID)
	if err != nil {
		a.themeAdminFailure(c, "load team theme selections", err)
		return
	}
	defer selectionRows.Close()
	var selections []adminTeamThemeSelection
	for selectionRows.Next() {
		var selection adminTeamThemeSelection
		if err = selectionRows.Scan(&selection.TeamID, &selection.TeamName, &selection.ThemeID, &selection.ThemePhrase); err != nil {
			a.themeAdminFailure(c, "scan team theme selection", err)
			return
		}
		selections = append(selections, selection)
	}
	if err = selectionRows.Err(); err != nil {
		a.themeAdminFailure(c, "iterate team theme selections", err)
		return
	}
	c.HTML(status, "admin_themes.html", adminThemesPageData{PageData: PageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: message, Ok: c.Query("ok")}, Jam: jam, Themes: themes, TeamSelections: selections})
}

func validateThemePhrase(raw string) (string, error) {
	phrase := strings.TrimSpace(raw)
	if utf8.RuneCountInString(phrase) < 1 || utf8.RuneCountInString(phrase) > 160 || hasControl(phrase) {
		return "", errors.New("Тема должна содержать от 1 до 160 символов без управляющих знаков.")
	}
	return phrase, nil
}

func lockThemeJam(ctx context.Context, tx pgx.Tx, jamID int64) error {
	var id int64
	return tx.QueryRow(ctx, `SELECT id FROM jams WHERE id=$1 FOR UPDATE`, jamID).Scan(&id)
}

func hasActiveTheme(ctx context.Context, tx pgx.Tx, jamID int64) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jam_themes WHERE jam_id=$1 AND withdrawn_at IS NULL)`, jamID).Scan(&exists)
	return exists, err
}

func loadThemeJamStage(ctx context.Context, tx pgx.Tx, jamID int64) (Stage, error) {
	var schedule Schedule
	var override *string
	err := tx.QueryRow(ctx, `
		SELECT submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override
		FROM jams WHERE id=$1`, jamID).Scan(&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err != nil {
		return "", err
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	return EffectiveStage(schedule, time.Now()), nil
}

func adminThemeIDs(c *gin.Context) (int64, int64, bool) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return 0, 0, false
	}
	themeID, ok := adminID(c, "themeID")
	return jamID, themeID, ok
}

func positiveFormID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id, err == nil && id > 0
}

func isThemePhraseConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "jam_themes_active_phrase_ci_unique"
}

func (a *App) handleThemeMutationError(c *gin.Context, jamID int64, operation string, err error) {
	if isThemePhraseConflict(err) {
		a.renderAdminThemes(c, jamID, "Активная тема с такой формулировкой уже существует в этом джеме.", http.StatusConflict)
		return
	}
	a.themeAdminFailure(c, operation, err)
}

func (a *App) handleThemeAdminLoadError(c *gin.Context, operation string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	a.themeAdminFailure(c, operation, err)
}

func (a *App) themeAdminFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	a.writeError(c, http.StatusInternalServerError, "Не удалось выполнить административное действие.")
}

func (a *App) themeSelectionFailure(c *gin.Context, publicTeamID string, err error) {
	a.logger.Error("select team theme", "error", err)
	themeSelectionRedirectError(c, publicTeamID, "Не удалось выбрать тему. Попробуйте позже.")
}

func themeSelectionRedirectError(c *gin.Context, publicTeamID, message string) {
	teamRedirectError(c, fmt.Sprintf("/teams/%s", publicTeamID), message)
}

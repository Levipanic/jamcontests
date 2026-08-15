package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type JamDateView struct {
	Label   string
	Moscow  string
	RFC3339 string
}

type archivePageData struct {
	User      *User
	CSRFToken string
	Jams      []JamView
}

func (a *App) jamDetail(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	jam, err := a.loadPublishedJam(c.Request.Context(), jamID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load public jam", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	data := PageData{User: CurrentUser(c), Jam: jam, AuthMode: "login", Next: c.Request.URL.Path}
	if err = a.populateJamPage(c, &data); err != nil {
		a.logger.Error("populate public jam", "error", err)
		data.Error = "Не удалось загрузить данные. Попробуйте позже."
	}
	currentJam, currentErr := a.loadPublishedJam(c.Request.Context(), jamID)
	if errors.Is(currentErr, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if currentErr != nil {
		a.logger.Error("recheck public jam", "error", currentErr)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if currentJam.Stage != jam.Stage {
		c.Redirect(http.StatusSeeOther, c.Request.URL.Path)
		return
	}
	a.render(c, http.StatusOK, "home.html", data)
}

func (a *App) archive(c *gin.Context) {
	jams, err := a.loadArchivedJams(c.Request.Context())
	if err != nil {
		a.logger.Error("load public jam archive", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	current, err := a.loadArchivedJams(c.Request.Context())
	if err != nil {
		a.logger.Error("recheck public jam archive", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if !samePublicJamSet(jams, current) {
		c.Redirect(http.StatusSeeOther, "/archive")
		return
	}
	c.HTML(http.StatusOK, "archive.html", archivePageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Jams: jams})
}

func samePublicJamSet(left, right []JamView) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Stage != right[index].Stage || left[index].Title != right[index].Title {
			return false
		}
	}
	return true
}

func (a *App) loadPublishedJam(ctx context.Context, jamID int64) (*JamView, error) {
	var jam JamView
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(ctx, `
		SELECT id, title, description, rules, max_team_size, submission_starts_at,
		       evaluation_starts_at, voting_starts_at, finishes_at, status_override
		FROM jams WHERE id=$1 AND visibility='published'`, jamID).Scan(
		&jam.ID, &jam.Title, &jam.Description, &jam.Rules, &jam.MaxTeamSize,
		&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt,
		&schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err != nil {
		return nil, err
	}
	applyPublicJamSchedule(&jam, schedule, override, time.Now())
	return &jam, nil
}

func (a *App) loadArchivedJams(ctx context.Context) ([]JamView, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT id, title, description, rules, max_team_size, submission_starts_at,
		       evaluation_starts_at, voting_starts_at, finishes_at, status_override
		FROM jams WHERE visibility='published' ORDER BY finishes_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var jams []JamView
	for rows.Next() {
		var jam JamView
		var schedule Schedule
		var override *string
		if err = rows.Scan(&jam.ID, &jam.Title, &jam.Description, &jam.Rules, &jam.MaxTeamSize,
			&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt,
			&schedule.VotingStartsAt, &schedule.FinishesAt, &override); err != nil {
			return nil, err
		}
		applyPublicJamSchedule(&jam, schedule, override, now)
		if jam.Stage == StageFinished {
			jams = append(jams, jam)
		}
	}
	return jams, rows.Err()
}

func applyPublicJamSchedule(jam *JamView, schedule Schedule, override *string, now time.Time) {
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	jam.Stage = EffectiveStage(schedule, now)
	jam.StageIndex = stageIndex(jam.Stage)
	if schedule.Override == nil && jam.Stage != StageFinished {
		jam.NextStageAt = NextBoundary(schedule, jam.Stage)
		if jam.NextStageAt != nil {
			jam.NextStageRFC3339 = jam.NextStageAt.UTC().Format(time.RFC3339)
		}
	}
	jam.Dates = []JamDateView{
		jamDate("Начало сдачи", schedule.SubmissionStartsAt),
		jamDate("Начало оценки", schedule.EvaluationStartsAt),
		jamDate("Начало голосования", schedule.VotingStartsAt),
		jamDate("Завершение", schedule.FinishesAt),
	}
}

func jamDate(label string, value time.Time) JamDateView {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		location = time.FixedZone("Europe/Moscow", 3*60*60)
	}
	return JamDateView{Label: label, Moscow: value.In(location).Format("02.01.2006 15:04 МСК"), RFC3339: value.UTC().Format(time.RFC3339)}
}

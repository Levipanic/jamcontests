package web

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type adminDashboardCounts struct {
	Teams         int
	Members       int
	Themes        int
	FinalProducts int
	Nominations   int
	Votes         int
	Bumps         int64
}

type adminDashboardJam struct {
	Jam               adminJam
	ThemeCount        int
	TeamCount         int
	FinalProductCount int
	// ReadyToPublish is only meaningful for drafts: both a question and an
	// active theme must exist before publication.
	ReadyToPublish bool
}

type adminDashboardData struct {
	User         *User
	CSRFToken    string
	Error        string
	Ok           string
	Jam          *adminJam
	Active       bool
	Counts       adminDashboardCounts
	Jams         []adminDashboardJam
	PublicJamID  string
	Warnings     []string
	ActiveAbsent bool
}

func (a *App) adminDashboard(c *gin.Context) {
	active, err := a.activeJam(c.Request.Context())
	if err != nil {
		a.logger.Error("load admin dashboard jam", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	data := adminDashboardData{
		User: CurrentUser(c), CSRFToken: csrfToken(c),
		Error: c.Query("error"), Ok: c.Query("ok"),
	}
	if active != nil {
		data.Jam, err = a.loadAdminJam(c.Request.Context(), active.ID)
		if err != nil {
			a.logger.Error("load admin dashboard active jam", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		data.Active = true
		if data.Jam.Visibility == "published" {
			if publicID, idErr := a.publicIDOf(c.Request.Context(), "jams", data.Jam.ID); idErr == nil {
				data.PublicJamID = publicID
			}
		}
		err = a.pool.QueryRow(c.Request.Context(), `
			SELECT (SELECT count(*) FROM teams WHERE jam_id=$1),
			       (SELECT count(*) FROM team_members WHERE jam_id=$1),
			       (SELECT count(*) FROM jam_themes WHERE jam_id=$1 AND withdrawn_at IS NULL),
			       (SELECT count(*) FROM products WHERE jam_id=$1 AND status='final'),
			       (SELECT count(*) FROM nominations WHERE jam_id=$1 AND withdrawn_at IS NULL),
			       (SELECT count(*) FROM nomination_votes WHERE jam_id=$1 AND invalidated_at IS NULL),
			       COALESCE((SELECT sum(bump_count-invalidated_count) FROM product_bumps WHERE jam_id=$1), 0)`, data.Jam.ID).Scan(
			&data.Counts.Teams, &data.Counts.Members, &data.Counts.Themes,
			&data.Counts.FinalProducts, &data.Counts.Nominations, &data.Counts.Votes, &data.Counts.Bumps)
		if err != nil {
			a.logger.Error("load admin dashboard counts", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	} else {
		data.ActiveAbsent = true
	}
	jams, err := a.loadAdminJams(c.Request.Context())
	if err != nil {
		a.logger.Error("load admin dashboard jams", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	volumeRows, err := a.pool.Query(c.Request.Context(), `
		SELECT j.id,
		       (SELECT count(*) FROM jam_themes theme WHERE theme.jam_id=j.id AND theme.withdrawn_at IS NULL),
		       (SELECT count(*) FROM teams team WHERE team.jam_id=j.id),
		       (SELECT count(*) FROM products product WHERE product.jam_id=j.id AND product.status='final')
		FROM jams j ORDER BY j.created_at DESC, j.id DESC`)
	if err != nil {
		a.logger.Error("load admin dashboard jam volumes", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer volumeRows.Close()
	volumes := make(map[int64]adminDashboardJam)
	for volumeRows.Next() {
		var row adminDashboardJam
		if err = volumeRows.Scan(&row.Jam.ID, &row.ThemeCount, &row.TeamCount, &row.FinalProductCount); err != nil {
			a.logger.Error("scan admin dashboard jam volumes", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		volumes[row.Jam.ID] = row
	}
	if err = volumeRows.Err(); err != nil {
		a.logger.Error("iterate admin dashboard jam volumes", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	for _, jam := range jams {
		row := adminDashboardJam{Jam: jam}
		if volume, exists := volumes[jam.ID]; exists {
			row.ThemeCount = volume.ThemeCount
			row.TeamCount = volume.TeamCount
			row.FinalProductCount = volume.FinalProductCount
		}
		if jam.Visibility == "draft" {
			row.ReadyToPublish = jam.QuestionCount > 0 && row.ThemeCount > 0
		}
		data.Jams = append(data.Jams, row)
	}
	data.Warnings = a.dashboardWarnings(data.Jams, now)
	c.HTML(http.StatusOK, "admin_dashboard.html", data)
}

// dashboardWarnings returns display-only readiness warnings. They never gate
// operations; the individual mutations perform their own authoritative checks.
func (a *App) dashboardWarnings(jams []adminDashboardJam, now time.Time) []string {
	var warnings []string
	hasActivePublished := false
	for _, row := range jams {
		jam := row.Jam
		stage := EffectiveStage(jam.Schedule, now)
		if jam.Visibility == "published" && stage != StageFinished {
			hasActivePublished = true
		}
		if jam.Visibility == "draft" && jam.QuestionCount == 0 {
			warnings = append(warnings, "Черновик «"+jam.Title+"»: нет ни одного вопроса анкеты, публикация невозможна.")
		}
		if row.ThemeCount == 0 && stage != StageUpcoming {
			warnings = append(warnings, "«"+jam.Title+"»: нет активных тем на стадии "+rusStageLabel(stage)+", выбор и сдача заблокированы.")
		}
		if jam.Visibility == "published" && stage == StageUpcoming && jam.QuestionCount == 0 && row.ThemeCount == 0 {
			warnings = append(warnings, "«"+jam.Title+"»: стадия «Сбор» уже началась, но вопросы и темы не подготовлены.")
		}
	}
	if !hasActivePublished {
		for _, row := range jams {
			if row.Jam.Visibility == "published" {
				warnings = append(warnings, "Сейчас нет опубликованного активного джема; опубликованные джемы находятся в архиве (Финал).")
				break
			}
		}
	}
	return warnings
}

func (a *App) loadDashboardJamCounts(c *gin.Context, jamID int64) (adminDashboardCounts, error) {
	var counts adminDashboardCounts
	err := a.pool.QueryRow(c.Request.Context(), `
		SELECT (SELECT count(*) FROM teams WHERE jam_id=$1),
		       (SELECT count(*) FROM team_members WHERE jam_id=$1),
		       (SELECT count(*) FROM jam_themes WHERE jam_id=$1 AND withdrawn_at IS NULL),
		       (SELECT count(*) FROM products WHERE jam_id=$1 AND status='final'),
		       (SELECT count(*) FROM nominations WHERE jam_id=$1 AND withdrawn_at IS NULL),
		       (SELECT count(*) FROM nomination_votes WHERE jam_id=$1 AND invalidated_at IS NULL),
		       COALESCE((SELECT sum(bump_count-invalidated_count) FROM product_bumps WHERE jam_id=$1), 0)`, jamID).Scan(
		&counts.Teams, &counts.Members, &counts.Themes,
		&counts.FinalProducts, &counts.Nominations, &counts.Votes, &counts.Bumps)
	return counts, err
}

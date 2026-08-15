package web

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
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

type adminDashboardData struct {
	User      *User
	CSRFToken string
	Error     string
	Jam       *adminJam
	Active    bool
	Counts    adminDashboardCounts
}

func (a *App) adminDashboard(c *gin.Context) {
	active, err := a.activeJam(c.Request.Context())
	if err != nil {
		a.logger.Error("load admin dashboard jam", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	data := adminDashboardData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: c.Query("error")}
	if active != nil {
		data.Jam, err = a.loadAdminJam(c.Request.Context(), active.ID)
		data.Active = true
	} else {
		var fallbackID int64
		err = a.pool.QueryRow(c.Request.Context(), `SELECT id FROM jams ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&fallbackID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		} else if err == nil {
			data.Jam, err = a.loadAdminJam(c.Request.Context(), fallbackID)
		}
	}
	if err != nil {
		a.logger.Error("load admin dashboard target jam", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if data.Jam != nil {
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
	}
	c.HTML(http.StatusOK, "admin_dashboard.html", data)
}

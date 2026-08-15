package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type NominationView struct {
	ID             int64
	Kind           string
	Title          string
	AuthorTeamName string
	Products       []VotingProductView
	Result         *NominationResultView
}

type NominationResultView struct {
	TotalVotes int64
	Winners    []NominationWinnerView
}

type NominationWinnerView struct {
	ProductID    int64
	ProductTitle string
	TeamID       int64
	TeamName     string
	VoteCount    int64
}

type VotingProductView struct {
	ID         int64
	Title      string
	TeamID     int64
	TeamName   string
	VoteCount  int64
	Selected   bool
	OwnProduct bool
}

type adminNomination struct {
	NominationView
	AuthorTeamID int64
	ProductID    int64
	ProductTitle string
	Withdrawn    bool
}

type nominationsPageData struct {
	User        *User
	CSRFToken   string
	JamID       int64
	JamTitle    string
	Stage       Stage
	Nominations []NominationView
}

type adminNominationsPageData struct {
	PageData
	Jam         *adminJam
	Nominations []adminNomination
	Mutable     bool
}

func (a *App) registerNominationRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("/jams/:id/nominations", a.adminNominationsPage)
	admin.POST("/jams/:id/nominations", a.adminNominationCreate)
	admin.POST("/jams/:id/nominations/:nominationID/edit", a.adminNominationEdit)
	admin.POST("/jams/:id/nominations/:nominationID/withdraw", a.adminNominationWithdraw)

	router.GET("/jams/:id/nominations", a.nominationsList)
}

func canDiscloseNominations(stage Stage) bool {
	return stage == StageVoting || stage == StageFinished
}

func canAdminMutateNominations(stage Stage) bool {
	return stage == StageUpcoming || stage == StageSubmission || stage == StageEvaluation
}

func (a *App) nominationsList(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	jamTitle, stage, err := a.loadPublishedJamStage(c.Request.Context(), jamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canDiscloseNominations(stage) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.nominationFailure(c, "load public nomination jam", err)
		return
	}
	query := `
		SELECT nomination.id, nomination.kind, nomination.title
		FROM nominations nomination
		WHERE nomination.jam_id=$1 AND nomination.withdrawn_at IS NULL
		  AND (nomination.kind='curator' OR EXISTS (
		      SELECT 1 FROM products product
		      WHERE product.id=nomination.product_id AND product.jam_id=nomination.jam_id
		        AND product.status='final'
		  ))
		ORDER BY nomination.created_at, nomination.id`
	if stage == StageFinished {
		query = `
			SELECT nomination.id, nomination.kind, nomination.title, COALESCE(team.name, '')
			FROM nominations nomination
			LEFT JOIN teams team ON team.id=nomination.author_team_id AND team.jam_id=nomination.jam_id
			WHERE nomination.jam_id=$1 AND nomination.withdrawn_at IS NULL
			  AND (nomination.kind='curator' OR EXISTS (
			      SELECT 1 FROM products product
			      WHERE product.id=nomination.product_id AND product.jam_id=nomination.jam_id
			        AND product.status='final'
			  ))
			ORDER BY nomination.created_at, nomination.id`
	}
	rows, err := a.pool.Query(c.Request.Context(), query, jamID)
	if err != nil {
		a.nominationFailure(c, "load public nominations", err)
		return
	}
	defer rows.Close()
	var nominations []NominationView
	for rows.Next() {
		var nomination NominationView
		if stage == StageFinished {
			err = rows.Scan(&nomination.ID, &nomination.Kind, &nomination.Title, &nomination.AuthorTeamName)
		} else {
			err = rows.Scan(&nomination.ID, &nomination.Kind, &nomination.Title)
		}
		if err != nil {
			a.nominationFailure(c, "scan public nomination", err)
			return
		}
		nominations = append(nominations, nomination)
	}
	if err = rows.Err(); err != nil {
		a.nominationFailure(c, "iterate public nominations", err)
		return
	}
	if stage == StageVoting {
		if err = a.populateVotingProducts(c.Request.Context(), jamID, CurrentUser(c), nominations); err != nil {
			a.nominationFailure(c, "load voting products", err)
			return
		}
	}
	if stage == StageFinished {
		if err = a.populateFinishedResults(c.Request.Context(), jamID, nominations); err != nil {
			a.nominationFailure(c, "load finished nomination results", err)
			return
		}
	}
	_, currentStage, err := a.loadPublishedJamStage(c.Request.Context(), jamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canDiscloseNominations(currentStage) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.nominationFailure(c, "recheck public nomination stage", err)
		return
	}
	if currentStage != stage {
		c.Redirect(http.StatusSeeOther, c.Request.URL.Path)
		return
	}
	c.HTML(http.StatusOK, "nominations_list.html", nominationsPageData{User: CurrentUser(c), CSRFToken: csrfToken(c), JamID: jamID, JamTitle: jamTitle, Stage: stage, Nominations: nominations})
}

func (a *App) populateFinishedResults(ctx context.Context, jamID int64, nominations []NominationView) error {
	indexes := make(map[int64]int, len(nominations))
	for index := range nominations {
		indexes[nominations[index].ID] = index
		nominations[index].Result = &NominationResultView{Winners: []NominationWinnerView{}}
	}
	rows, err := a.pool.Query(ctx, `
		WITH public_nominations AS (
			SELECT nomination.id
			FROM nominations nomination
			JOIN jams jam ON jam.id=nomination.jam_id AND jam.visibility='published'
			WHERE nomination.jam_id=$1 AND nomination.withdrawn_at IS NULL
			  AND CASE
			      WHEN jam.status_override IS NOT NULL THEN jam.status_override='finished'
			      ELSE clock_timestamp() >= jam.finishes_at
			  END
			  AND (nomination.kind='curator' OR EXISTS (
			      SELECT 1 FROM products author_product
			      WHERE author_product.id=nomination.product_id
			        AND author_product.jam_id=nomination.jam_id
			        AND author_product.status='final'
			  ))
		), vote_counts AS (
			SELECT vote.nomination_id, vote.product_id, count(*)::bigint AS vote_count
			FROM nomination_votes vote
			JOIN public_nominations nomination ON nomination.id=vote.nomination_id
			JOIN products product ON product.id=vote.product_id AND product.jam_id=vote.jam_id
			  AND product.status='final'
			WHERE vote.jam_id=$1
			GROUP BY vote.nomination_id, vote.product_id
		), maxima AS (
			SELECT nomination_id, max(vote_count) AS max_count, sum(vote_count)::bigint AS total_votes
			FROM vote_counts GROUP BY nomination_id
		)
		SELECT nomination.id, COALESCE(maxima.total_votes, 0), winner.product_id,
		       product.title, product.team_id, team.name, winner.vote_count
		FROM public_nominations nomination
		LEFT JOIN maxima ON maxima.nomination_id=nomination.id
		LEFT JOIN vote_counts winner ON winner.nomination_id=nomination.id
		  AND winner.vote_count=maxima.max_count AND maxima.total_votes > 0
		LEFT JOIN products product ON product.id=winner.product_id AND product.jam_id=$1
		LEFT JOIN teams team ON team.id=product.team_id AND team.jam_id=product.jam_id
		ORDER BY nomination.id, product.finalized_at, product.id`, jamID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var nominationID, totalVotes int64
		var productID, teamID, voteCount *int64
		var productTitle, teamName *string
		if err = rows.Scan(&nominationID, &totalVotes, &productID, &productTitle, &teamID, &teamName, &voteCount); err != nil {
			return err
		}
		index, ok := indexes[nominationID]
		if !ok {
			continue
		}
		nominations[index].Result.TotalVotes = totalVotes
		if productID != nil && productTitle != nil && teamID != nil && teamName != nil && voteCount != nil {
			nominations[index].Result.Winners = append(nominations[index].Result.Winners, NominationWinnerView{
				ProductID: *productID, ProductTitle: *productTitle, TeamID: *teamID,
				TeamName: *teamName, VoteCount: *voteCount,
			})
		}
	}
	return rows.Err()
}

func (a *App) populateVotingProducts(ctx context.Context, jamID int64, user *User, nominations []NominationView) error {
	var userID any
	if user != nil {
		userID = user.ID
	}
	rows, err := a.pool.Query(ctx, `
		SELECT product.id, product.title, team.id, team.name,
		       EXISTS (
		           SELECT 1 FROM team_members member
		           WHERE member.jam_id=product.jam_id AND member.team_id=product.team_id
		             AND member.user_id=$2::bigint
		       )
		FROM products product
		JOIN teams team ON team.id=product.team_id AND team.jam_id=product.jam_id
		JOIN jams jam ON jam.id=product.jam_id AND jam.visibility='published'
		WHERE product.jam_id=$1 AND product.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL THEN jam.status_override='voting'
		      ELSE clock_timestamp() >= jam.voting_starts_at AND clock_timestamp() < jam.finishes_at
		  END
		ORDER BY product.finalized_at, product.id`, jamID, userID)
	if err != nil {
		return err
	}
	var products []VotingProductView
	for rows.Next() {
		var product VotingProductView
		if err = rows.Scan(&product.ID, &product.Title, &product.TeamID, &product.TeamName, &product.OwnProduct); err != nil {
			rows.Close()
			return err
		}
		products = append(products, product)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	type voteKey struct {
		nominationID int64
		productID    int64
	}
	counts := make(map[voteKey]int64)
	selected := make(map[voteKey]bool)
	rows, err = a.pool.Query(ctx, `
		SELECT vote.nomination_id, vote.product_id, count(*)::bigint,
		       COALESCE(bool_or(vote.user_id=$2::bigint), false)
		FROM nomination_votes vote
		JOIN nominations nomination ON nomination.id=vote.nomination_id AND nomination.jam_id=vote.jam_id
		JOIN products product ON product.id=vote.product_id AND product.jam_id=vote.jam_id
		JOIN jams jam ON jam.id=vote.jam_id AND jam.visibility='published'
		WHERE vote.jam_id=$1 AND nomination.withdrawn_at IS NULL AND product.status='final'
		  AND (nomination.kind='curator' OR EXISTS (
		      SELECT 1 FROM products author_product
		      WHERE author_product.id=nomination.product_id AND author_product.jam_id=nomination.jam_id
		        AND author_product.status='final'
		  ))
		  AND CASE
		      WHEN jam.status_override IS NOT NULL THEN jam.status_override='voting'
		      ELSE clock_timestamp() >= jam.voting_starts_at AND clock_timestamp() < jam.finishes_at
		  END
		GROUP BY vote.nomination_id, vote.product_id`, jamID, userID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key voteKey
		var count int64
		var isSelected bool
		if err = rows.Scan(&key.nominationID, &key.productID, &count, &isSelected); err != nil {
			rows.Close()
			return err
		}
		counts[key] = count
		selected[key] = isSelected
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for index := range nominations {
		nominations[index].Products = make([]VotingProductView, len(products))
		copy(nominations[index].Products, products)
		for productIndex := range nominations[index].Products {
			key := voteKey{nominationID: nominations[index].ID, productID: nominations[index].Products[productIndex].ID}
			nominations[index].Products[productIndex].VoteCount = counts[key]
			nominations[index].Products[productIndex].Selected = selected[key]
		}
	}
	return nil
}

func (a *App) adminNominationsPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	a.renderAdminNominations(c, jamID, c.Query("error"), http.StatusOK)
}

func (a *App) adminNominationCreate(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	title := normalizeNominationTitle(c.PostForm("title"))
	if err := validateNominationTitle(title, false); err != nil {
		a.renderAdminNominations(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderAdminNominations(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminNominationFailure(c, "begin curator nomination creation", err)
		return
	}
	defer tx.Rollback(ctx)
	stage, err := lockNominationJam(ctx, tx, jamID)
	if err != nil {
		a.handleAdminNominationLoadError(c, err)
		return
	}
	if !canAdminMutateNominations(stage) {
		c.String(http.StatusConflict, "После начала голосования номинации изменять нельзя.")
		return
	}
	var nominationID int64
	if err = tx.QueryRow(ctx, `INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', $2) RETURNING id`, jamID, title).Scan(&nominationID); err != nil {
		a.adminNominationFailure(c, "create curator nomination", err)
		return
	}
	after := nominationAuditData(nominationID, jamID, "curator", title, 0, 0, false)
	if err = insertAdminAudit(ctx, tx, CurrentUser(c), "nomination.create", "nomination", nominationID, reason, nil, after); err != nil {
		a.adminNominationFailure(c, "audit curator nomination creation", err)
		return
	}
	open, err := adminNominationMutationOpen(ctx, tx, jamID)
	if err != nil {
		a.adminNominationFailure(c, "recheck curator nomination deadline", err)
		return
	}
	if !open {
		c.String(http.StatusConflict, "После начала голосования номинации изменять нельзя.")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminNominationFailure(c, "commit curator nomination creation", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/nominations", jamID))
}

func (a *App) adminNominationEdit(c *gin.Context) {
	jamID, nominationID, ok := adminNominationIDs(c)
	if !ok {
		return
	}
	title := normalizeNominationTitle(c.PostForm("title"))
	if err := validateNominationTitle(title, false); err != nil {
		a.renderAdminNominations(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderAdminNominations(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.mutateCuratorNomination(c, jamID, nominationID, title, reason, false)
}

func (a *App) adminNominationWithdraw(c *gin.Context) {
	jamID, nominationID, ok := adminNominationIDs(c)
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderAdminNominations(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if c.PostForm("confirm") != "withdraw" {
		a.renderAdminNominations(c, jamID, "Подтвердите отзыв номинации.", http.StatusUnprocessableEntity)
		return
	}
	a.mutateCuratorNomination(c, jamID, nominationID, "", reason, true)
}

func (a *App) mutateCuratorNomination(c *gin.Context, jamID, nominationID int64, title, reason string, withdraw bool) {
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminNominationFailure(c, "begin curator nomination mutation", err)
		return
	}
	defer tx.Rollback(ctx)
	stage, err := lockNominationJam(ctx, tx, jamID)
	if err != nil {
		a.handleAdminNominationLoadError(c, err)
		return
	}
	if !canAdminMutateNominations(stage) {
		c.String(http.StatusConflict, "После начала голосования номинации изменять нельзя.")
		return
	}
	var beforeTitle string
	var withdrawn bool
	err = tx.QueryRow(ctx, `SELECT title, withdrawn_at IS NOT NULL FROM nominations WHERE id=$1 AND jam_id=$2 AND kind='curator' FOR UPDATE`, nominationID, jamID).Scan(&beforeTitle, &withdrawn)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.adminNominationFailure(c, "lock curator nomination", err)
		return
	}
	if withdrawn {
		c.String(http.StatusConflict, "Отозванную номинацию нельзя изменять.")
		return
	}
	if !withdraw && title == beforeTitle {
		c.String(http.StatusConflict, "Название номинации не изменилось.")
		return
	}
	before := nominationAuditData(nominationID, jamID, "curator", beforeTitle, 0, 0, false)
	action, afterTitle := "nomination.edit", title
	if withdraw {
		action, afterTitle = "nomination.withdraw", beforeTitle
		_, err = tx.Exec(ctx, `UPDATE nominations SET withdrawn_at=now(), updated_at=now() WHERE id=$1`, nominationID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE nominations SET title=$2, updated_at=now() WHERE id=$1`, nominationID, title)
	}
	if err != nil {
		a.adminNominationFailure(c, "update curator nomination", err)
		return
	}
	after := nominationAuditData(nominationID, jamID, "curator", afterTitle, 0, 0, withdraw)
	if err = insertAdminAudit(ctx, tx, CurrentUser(c), action, "nomination", nominationID, reason, before, after); err != nil {
		a.adminNominationFailure(c, "audit curator nomination mutation", err)
		return
	}
	open, err := adminNominationMutationOpen(ctx, tx, jamID)
	if err != nil {
		a.adminNominationFailure(c, "recheck curator nomination deadline", err)
		return
	}
	if !open {
		c.String(http.StatusConflict, "После начала голосования номинации изменять нельзя.")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminNominationFailure(c, "commit curator nomination mutation", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/nominations", jamID))
}

func lockNominationJam(ctx context.Context, tx pgx.Tx, jamID int64) (Stage, error) {
	var schedule Schedule
	var override *string
	err := tx.QueryRow(ctx, `SELECT submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override FROM jams WHERE id=$1 FOR UPDATE`, jamID).Scan(&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err != nil {
		return "", err
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	return EffectiveStage(schedule, time.Now()), nil
}

func adminNominationMutationOpen(ctx context.Context, tx pgx.Tx, jamID int64) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT status_override IN ('upcoming', 'submission', 'evaluation')
			OR (status_override IS NULL AND clock_timestamp() < voting_starts_at)
		FROM jams WHERE id=$1`, jamID).Scan(&open)
	return open, err
}

func (a *App) renderAdminNominations(c *gin.Context, jamID int64, message string, status int) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load admin nomination jam", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT nomination.id, nomination.kind, nomination.title,
		       COALESCE(nomination.author_team_id, 0), COALESCE(team.name, ''),
		       COALESCE(nomination.product_id, 0), COALESCE(product.title, ''),
		       nomination.withdrawn_at IS NOT NULL
		FROM nominations nomination
		LEFT JOIN teams team ON team.id=nomination.author_team_id AND team.jam_id=nomination.jam_id
		LEFT JOIN products product ON product.id=nomination.product_id AND product.jam_id=nomination.jam_id
		WHERE nomination.jam_id=$1 ORDER BY nomination.created_at, nomination.id`, jamID)
	if err != nil {
		a.adminNominationFailure(c, "load admin nominations", err)
		return
	}
	defer rows.Close()
	var nominations []adminNomination
	for rows.Next() {
		var nomination adminNomination
		if err = rows.Scan(&nomination.ID, &nomination.Kind, &nomination.Title, &nomination.AuthorTeamID, &nomination.AuthorTeamName, &nomination.ProductID, &nomination.ProductTitle, &nomination.Withdrawn); err != nil {
			a.adminNominationFailure(c, "scan admin nomination", err)
			return
		}
		nominations = append(nominations, nomination)
	}
	if err = rows.Err(); err != nil {
		a.adminNominationFailure(c, "iterate admin nominations", err)
		return
	}
	c.HTML(status, "admin_nominations.html", adminNominationsPageData{PageData: PageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: message}, Jam: jam, Nominations: nominations, Mutable: canAdminMutateNominations(jam.Stage)})
}

func normalizeNominationTitle(value string) string {
	return strings.TrimSpace(value)
}

func nominationAuditData(id, jamID int64, kind, title string, teamID, productID int64, withdrawn bool) map[string]any {
	return map[string]any{"id": id, "jam_id": jamID, "kind": kind, "title": title, "author_team_id": teamID, "product_id": productID, "withdrawn": withdrawn}
}

func adminNominationIDs(c *gin.Context) (int64, int64, bool) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return 0, 0, false
	}
	nominationID, ok := adminID(c, "nominationID")
	return jamID, nominationID, ok
}

func (a *App) handleAdminNominationLoadError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	a.adminNominationFailure(c, "load nomination data", err)
}

func (a *App) adminNominationFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.String(http.StatusInternalServerError, "Не удалось выполнить административное действие с номинацией.")
}

func (a *App) nominationFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.AbortWithStatus(http.StatusInternalServerError)
}

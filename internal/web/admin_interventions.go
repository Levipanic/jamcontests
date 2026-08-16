package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type adminVoteRecord struct {
	ID                                                int64
	Username, NominationTitle, ProductTitle, TeamName string
	Invalidated                                       bool
	Reason                                            string
	UpdatedAt                                         time.Time
}

type adminBumpRecord struct {
	ID                               int64
	Username, ProductTitle, TeamName string
	BumpCount, InvalidatedCount      int64
	HasActive                        bool
	Reason                           string
	LastBumpedAt                     time.Time
}

type adminInterventionData struct {
	PageData
	Jam   *adminJam
	Votes []adminVoteRecord
	Bumps []adminBumpRecord
	Pager *adminPager
}

func (a *App) registerAdminInterventionRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("/jams/:id/votes", a.adminVotesPage)
	admin.POST("/jams/:id/votes/:recordID/invalidate", a.adminVoteInvalidate)
	admin.POST("/jams/:id/votes/:recordID/restore", a.adminVoteRestore)
	admin.GET("/jams/:id/bumps", a.adminBumpsPage)
	admin.POST("/jams/:id/bumps/:recordID/invalidate", a.adminBumpInvalidate)
	admin.POST("/jams/:id/bumps/:recordID/restore", a.adminBumpRestore)
}

func (a *App) adminVotesPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load admin votes jam", err)
		return
	}
	page, per := adminPageParam(c)
	var total int
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT count(*) FROM nomination_votes WHERE jam_id=$1`, jamID).Scan(&total); err != nil {
		a.adminInterventionFailure(c, "count admin votes", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT vote.id, user_account.username, nomination.title, product.title, team.name,
		       vote.invalidated_at IS NOT NULL, COALESCE(vote.invalidation_reason, ''), vote.updated_at
		FROM nomination_votes vote JOIN users user_account ON user_account.id=vote.user_id
		JOIN nominations nomination ON nomination.id=vote.nomination_id AND nomination.jam_id=vote.jam_id
		JOIN products product ON product.id=vote.product_id AND product.jam_id=vote.jam_id
		JOIN teams team ON team.id=product.team_id WHERE vote.jam_id=$1 ORDER BY nomination.id, vote.id OFFSET $2 LIMIT $3`, jamID, (page-1)*per, per)
	if err != nil {
		a.adminInterventionFailure(c, "load admin votes", err)
		return
	}
	defer rows.Close()
	data := adminInterventionData{PageData: PageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: c.Query("error"), Ok: c.Query("ok")}, Jam: jam, Pager: buildAdminPager(fmt.Sprintf("/admin/jams/%d/votes", jamID), page, per, total)}
	for rows.Next() {
		var row adminVoteRecord
		if err = rows.Scan(&row.ID, &row.Username, &row.NominationTitle, &row.ProductTitle, &row.TeamName, &row.Invalidated, &row.Reason, &row.UpdatedAt); err != nil {
			a.adminInterventionFailure(c, "scan admin votes", err)
			return
		}
		data.Votes = append(data.Votes, row)
	}
	if err = rows.Err(); err != nil {
		a.adminInterventionFailure(c, "iterate admin votes", err)
		return
	}
	c.HTML(http.StatusOK, "admin_votes.html", data)
}

func (a *App) adminBumpsPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load admin bumps jam", err)
		return
	}
	page, per := adminPageParam(c)
	var total int
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT count(*) FROM product_bumps WHERE jam_id=$1`, jamID).Scan(&total); err != nil {
		a.adminInterventionFailure(c, "count admin bumps", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT bump.id,user_account.username,product.title,team.name,bump.bump_count,bump.invalidated_count,COALESCE(bump.invalidation_reason,''),bump.last_bumped_at FROM product_bumps bump JOIN users user_account ON user_account.id=bump.user_id JOIN products product ON product.id=bump.product_id AND product.jam_id=bump.jam_id JOIN teams team ON team.id=product.team_id WHERE bump.jam_id=$1 ORDER BY product.id,bump.id OFFSET $2 LIMIT $3`, jamID, (page-1)*per, per)
	if err != nil {
		a.adminInterventionFailure(c, "load admin bumps", err)
		return
	}
	defer rows.Close()
	data := adminInterventionData{PageData: PageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: c.Query("error"), Ok: c.Query("ok")}, Jam: jam, Pager: buildAdminPager(fmt.Sprintf("/admin/jams/%d/bumps", jamID), page, per, total)}
	for rows.Next() {
		var row adminBumpRecord
		if err = rows.Scan(&row.ID, &row.Username, &row.ProductTitle, &row.TeamName, &row.BumpCount, &row.InvalidatedCount, &row.Reason, &row.LastBumpedAt); err != nil {
			a.adminInterventionFailure(c, "scan admin bumps", err)
			return
		}
		row.HasActive = row.BumpCount > row.InvalidatedCount
		data.Bumps = append(data.Bumps, row)
	}
	if err = rows.Err(); err != nil {
		a.adminInterventionFailure(c, "iterate admin bumps", err)
		return
	}
	c.HTML(http.StatusOK, "admin_bumps.html", data)
}

func (a *App) adminVoteInvalidate(c *gin.Context) { a.adminMutateVote(c, true) }
func (a *App) adminVoteRestore(c *gin.Context)    { a.adminMutateVote(c, false) }

func (a *App) adminMutateVote(c *gin.Context, invalidate bool) {
	jamID, recordID, ok := adminInterventionIDs(c)
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.adminInterventionRedirect(c, jamID, "votes", err.Error())
		return
	}
	expected := "invalidate"
	if !invalidate {
		expected = "restore"
	}
	if c.PostForm("confirm") != expected {
		a.adminInterventionRedirect(c, jamID, "votes", "Подтвердите вмешательство.")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminInterventionFailure(c, "begin vote intervention", err)
		return
	}
	defer tx.Rollback(ctx)
	var userID, nominationID, productID int64
	if err = tx.QueryRow(ctx, `SELECT user_id FROM nomination_votes WHERE id=$1 AND jam_id=$2`, recordID, jamID).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	} else if err != nil {
		a.adminInterventionFailure(c, "load vote intervention user", err)
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(format('vote-membership:%s:%s', $1::bigint, $2::bigint), 0))`, jamID, userID); err != nil {
		a.adminInterventionFailure(c, "lock vote membership", err)
		return
	}
	var invalidatedAt *time.Time
	var invalidatedBy *int64
	var beforeReason *string
	err = tx.QueryRow(ctx, `SELECT user_id,nomination_id,product_id,invalidated_at,invalidated_by,invalidation_reason FROM nomination_votes WHERE id=$1 AND jam_id=$2 FOR UPDATE`, recordID, jamID).Scan(&userID, &nominationID, &productID, &invalidatedAt, &invalidatedBy, &beforeReason)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.adminInterventionFailure(c, "lock vote intervention", err)
		return
	}
	if invalidate == (invalidatedAt != nil) {
		a.adminInterventionRedirect(c, jamID, "votes", "Голос уже имеет выбранное состояние.")
		return
	}
	if !invalidate {
		var restorable bool
		if err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM nominations nomination
				JOIN products product ON product.id=$3 AND product.jam_id=nomination.jam_id
				JOIN products nomination_product
				  ON nomination_product.id=CASE WHEN nomination.kind='team' THEN nomination.product_id ELSE product.id END
				 AND nomination_product.jam_id=nomination.jam_id AND nomination_product.status='final'
				WHERE nomination.id=$2 AND nomination.jam_id=$1 AND nomination.withdrawn_at IS NULL
				  AND product.status='final' AND NOT EXISTS (
					SELECT 1 FROM team_members member
					WHERE member.jam_id=$1 AND member.user_id=$4 AND member.team_id=product.team_id
				  )
			)`, jamID, nominationID, productID, userID).Scan(&restorable); err != nil {
			a.adminInterventionFailure(c, "validate vote restoration", err)
			return
		}
		if !restorable {
			a.adminInterventionRedirect(c, jamID, "votes", "Голос нельзя восстановить при текущем продукте, номинации или составе команды.")
			return
		}
	}
	before := map[string]any{"id": recordID, "user_id": userID, "nomination_id": nominationID, "product_id": productID, "invalidated_at": invalidatedAt, "invalidated_by": invalidatedBy, "invalidation_reason": beforeReason}
	if invalidate {
		err = tx.QueryRow(ctx, `UPDATE nomination_votes SET invalidated_at=clock_timestamp(),invalidated_by=$2,invalidation_reason=$3 WHERE id=$1 RETURNING invalidated_at,invalidated_by,invalidation_reason`, recordID, CurrentUser(c).ID, reason).Scan(&invalidatedAt, &invalidatedBy, &beforeReason)
	} else {
		err = tx.QueryRow(ctx, `UPDATE nomination_votes SET invalidated_at=NULL,invalidated_by=NULL,invalidation_reason=NULL WHERE id=$1 RETURNING invalidated_at,invalidated_by,invalidation_reason`, recordID).Scan(&invalidatedAt, &invalidatedBy, &beforeReason)
	}
	if err != nil {
		if isUniqueViolation(err) {
			a.adminInterventionRedirect(c, jamID, "votes", "У пользователя уже есть активный голос в этой номинации.")
			return
		}
		a.adminInterventionFailure(c, "update vote intervention", err)
		return
	}
	after := map[string]any{"id": recordID, "user_id": userID, "nomination_id": nominationID, "product_id": productID, "invalidated_at": invalidatedAt, "invalidated_by": invalidatedBy, "invalidation_reason": beforeReason}
	action := "vote.restore"
	if invalidate {
		action = "vote.invalidate"
	}
	if err = insertAdminAudit(ctx, tx, CurrentUser(c), action, "nomination_vote", recordID, reason, before, after); err != nil {
		a.adminInterventionFailure(c, "audit vote intervention", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminInterventionFailure(c, "commit vote intervention", err)
		return
	}
	message := "Голос восстановлен."
	if invalidate {
		message = "Голос инвалидирован."
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/votes", jamID), message)
}

func (a *App) adminBumpInvalidate(c *gin.Context) { a.adminMutateBump(c, true) }
func (a *App) adminBumpRestore(c *gin.Context)    { a.adminMutateBump(c, false) }
func (a *App) adminMutateBump(c *gin.Context, invalidate bool) {
	jamID, recordID, ok := adminInterventionIDs(c)
	if !ok {
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.adminInterventionRedirect(c, jamID, "bumps", err.Error())
		return
	}
	expected := "invalidate"
	if !invalidate {
		expected = "restore"
	}
	if c.PostForm("confirm") != expected {
		a.adminInterventionRedirect(c, jamID, "bumps", "Подтвердите вмешательство.")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminInterventionFailure(c, "begin bump intervention", err)
		return
	}
	defer tx.Rollback(ctx)
	var userID, productID, count, invalidated int64
	var invalidatedAt *time.Time
	var invalidatedBy *int64
	var invalidationReason *string
	err = tx.QueryRow(ctx, `SELECT user_id,product_id,bump_count,invalidated_count,invalidated_at,invalidated_by,invalidation_reason FROM product_bumps WHERE id=$1 AND jam_id=$2 FOR UPDATE`, recordID, jamID).Scan(&userID, &productID, &count, &invalidated, &invalidatedAt, &invalidatedBy, &invalidationReason)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.adminInterventionFailure(c, "lock bump intervention", err)
		return
	}
	if (invalidate && invalidated == count) || (!invalidate && invalidated == 0) {
		a.adminInterventionRedirect(c, jamID, "bumps", "Бампы уже имеют выбранное состояние.")
		return
	}
	before := map[string]any{"id": recordID, "user_id": userID, "product_id": productID, "bump_count": count, "invalidated_count": invalidated, "invalidated_at": invalidatedAt, "invalidated_by": invalidatedBy, "invalidation_reason": invalidationReason}
	if invalidate {
		err = tx.QueryRow(ctx, `UPDATE product_bumps SET invalidated_count=bump_count,invalidated_at=clock_timestamp(),invalidated_by=$2,invalidation_reason=$3 WHERE id=$1 RETURNING invalidated_count,invalidated_at,invalidated_by,invalidation_reason`, recordID, CurrentUser(c).ID, reason).Scan(&invalidated, &invalidatedAt, &invalidatedBy, &invalidationReason)
	} else {
		err = tx.QueryRow(ctx, `UPDATE product_bumps SET invalidated_count=0,invalidated_at=NULL,invalidated_by=NULL,invalidation_reason=NULL WHERE id=$1 RETURNING invalidated_count,invalidated_at,invalidated_by,invalidation_reason`, recordID).Scan(&invalidated, &invalidatedAt, &invalidatedBy, &invalidationReason)
	}
	if err != nil {
		a.adminInterventionFailure(c, "update bump intervention", err)
		return
	}
	after := map[string]any{"id": recordID, "user_id": userID, "product_id": productID, "bump_count": count, "invalidated_count": invalidated, "invalidated_at": invalidatedAt, "invalidated_by": invalidatedBy, "invalidation_reason": invalidationReason}
	action := "bump.restore"
	if invalidate {
		action = "bump.invalidate"
	}
	if err = insertAdminAudit(ctx, tx, CurrentUser(c), action, "product_bump", recordID, reason, before, after); err != nil {
		a.adminInterventionFailure(c, "audit bump intervention", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminInterventionFailure(c, "commit bump intervention", err)
		return
	}
	message := "Бампы восстановлены."
	if invalidate {
		message = "Бампы исключены из публичного счётчика."
	}
	adminOkRedirect(c, fmt.Sprintf("/admin/jams/%d/bumps", jamID), message)
}

func adminInterventionIDs(c *gin.Context) (int64, int64, bool) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return 0, 0, false
	}
	recordID, ok := adminID(c, "recordID")
	return jamID, recordID, ok
}
func (a *App) adminInterventionRedirect(c *gin.Context, jamID int64, domain, message string) {
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/%s?error=%s", jamID, domain, urlQueryEscape(message)))
}
func (a *App) adminInterventionFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	a.writeError(c, http.StatusInternalServerError, "Не удалось выполнить административное вмешательство.")
}

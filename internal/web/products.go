package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const (
	productTitleMax       = 200
	productURLMax         = 2048
	productDescriptionMax = 5000
	productNotesMax       = 5000
	nominationTitleMax    = 160
)

var (
	errProductIncomplete = errors.New("Для финальной сдачи укажите название и ссылку на результат.")
	errProductIneligible = errors.New("Финальная сдача доступна только команде с подтверждённым допуском.")
	errProductTheme      = errors.New("Для финальной сдачи выберите активную тему этого джема.")
)

type ProductView struct {
	ID              int64
	JamID           int64
	JamTitle        string
	TeamID          int64
	TeamName        string
	Title           string
	ResultURL       string
	Description     string
	CommentaryURL   string
	Notes           string
	Status          string
	Theme           string
	NominationTitle string
	BumpCount       int64
}

type productPageData struct {
	User         *User
	CSRFToken    string
	Error        string
	JamID        int64
	JamTitle     string
	TeamID       int64
	TeamName     string
	Product      ProductView
	Stage        Stage
	BumpsMutable bool
}

type productsListPageData struct {
	User         *User
	CSRFToken    string
	JamID        int64
	JamTitle     string
	Products     []ProductView
	Stage        Stage
	BumpsMutable bool
}

type adminProductsPageData struct {
	PageData
	Jam      *adminJam
	Products []ProductView
}

type productTeamRecord struct {
	ID                 int64
	JamID              int64
	Name               string
	CaptainID          int64
	ProductEditor      bool
	SubmissionStartsAt time.Time
	EvaluationStartsAt time.Time
	VotingStartsAt     time.Time
	FinishesAt         time.Time
	Override           *string
}

func (a *App) registerProductRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("/jams/:id/products", a.adminProductsPage)
	admin.POST("/jams/:id/products/:productID/update", a.adminProductUpdate)

	router.GET("/jams/:id/product", RequireAuth(), a.productEditPage)
	router.POST("/jams/:id/product/save", RequireAuth(), a.productSave)
	router.POST("/jams/:id/product/finalize", RequireAuth(), a.productFinalize)
	router.GET("/jams/:id/products", a.productsList)
	router.GET("/products/:id", a.productDetail)
}

func (a *App) productEditPage(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	team, err := a.loadEditableProductTeam(c.Request.Context(), jamID, CurrentUser(c).ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canEditProduct(team, CurrentUser(c).ID) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.productFailure(c, "load product editor", err)
		return
	}
	var product ProductView
	var commentary *string
	err = a.pool.QueryRow(c.Request.Context(), `
		SELECT product.id, product.title, product.result_url, product.description,
		       product.commentary_url, product.notes, product.status,
		       COALESCE(nomination.title, '')
		FROM products product
		LEFT JOIN nominations nomination ON nomination.product_id=product.id
			AND nomination.kind='team' AND nomination.withdrawn_at IS NULL
		WHERE product.team_id=$1 AND product.jam_id=$2`, team.ID, jamID).Scan(
		&product.ID, &product.Title, &product.ResultURL, &product.Description, &commentary,
		&product.Notes, &product.Status, &product.NominationTitle)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.productFailure(c, "load product draft", err)
		return
	}
	if commentary != nil {
		product.CommentaryURL = *commentary
	}
	if product.Status == "" {
		product.Status = "draft"
	}
	a.renderProductEdit(c, http.StatusOK, team, product, c.Query("error"))
}

func (a *App) productSave(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	product, err := productFromForm(c)
	validationErr := err
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.productMutationFailure(c, jamID, "begin product save", err)
		return
	}
	defer tx.Rollback(ctx)
	team, err := lockProductTeam(ctx, tx, jamID, CurrentUser(c).ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canEditProduct(team, CurrentUser(c).ID) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.productMutationFailure(c, jamID, "lock product team", err)
		return
	}
	if validationErr != nil {
		productRedirectError(c, jamID, validationErr.Error())
		return
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM products WHERE team_id=$1 AND jam_id=$2`, team.ID, jamID).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.productMutationFailure(c, jamID, "load product status", err)
		return
	}
	if status == "final" {
		if err = validateFinalProductTx(ctx, tx, team, product); err != nil {
			a.handleFinalProductError(c, jamID, "validate final product edit", err)
			return
		}
	}
	if !canEditProduct(team, CurrentUser(c).ID) {
		productRedirectError(c, jamID, "Редактирование продукта уже закрыто.")
		return
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO products (jam_id, team_id, title, result_url, description, commentary_url, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (team_id, jam_id) DO UPDATE SET
			title=EXCLUDED.title, result_url=EXCLUDED.result_url, description=EXCLUDED.description,
			commentary_url=EXCLUDED.commentary_url, notes=EXCLUDED.notes, updated_at=now()
		RETURNING id`, jamID, team.ID, product.Title, product.ResultURL, product.Description,
		nullableProductURL(product.CommentaryURL), product.Notes).Scan(&product.ID)
	if err != nil {
		a.productMutationFailure(c, jamID, "save product", err)
		return
	}
	if !canEditProduct(team, CurrentUser(c).ID) {
		productRedirectError(c, jamID, "Редактирование продукта уже закрыто.")
		return
	}
	if product.NominationTitle == "" {
		_, err = tx.Exec(ctx, `
			UPDATE nominations SET withdrawn_at=now(), updated_at=now()
			WHERE jam_id=$1 AND product_id=$2 AND kind='team' AND withdrawn_at IS NULL`, jamID, product.ID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO nominations (jam_id, kind, title, author_team_id, product_id)
			VALUES ($1, 'team', $2, $3, $4)
			ON CONFLICT (jam_id, product_id) WHERE kind='team' DO UPDATE SET
				title=EXCLUDED.title, withdrawn_at=NULL, updated_at=now()`,
			jamID, product.NominationTitle, team.ID, product.ID)
	}
	if err != nil {
		a.productMutationFailure(c, jamID, "save team nomination", err)
		return
	}
	open, err := teamNominationMutationOpen(ctx, tx, jamID)
	if err != nil {
		a.productMutationFailure(c, jamID, "recheck team nomination deadline", err)
		return
	}
	if !open {
		productRedirectError(c, jamID, "Редактирование продукта уже закрыто.")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.productMutationFailure(c, jamID, "commit product save", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/jams/%d/product", jamID))
}

func teamNominationMutationOpen(ctx context.Context, tx pgx.Tx, jamID int64) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT visibility='published' AND (
			status_override='submission'
			OR (status_override IS NULL AND clock_timestamp() >= submission_starts_at AND clock_timestamp() < evaluation_starts_at)
		)
		FROM jams WHERE id=$1`, jamID).Scan(&open)
	return open, err
}

func (a *App) productFinalize(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.productMutationFailure(c, jamID, "begin product finalization", err)
		return
	}
	defer tx.Rollback(ctx)
	team, err := lockProductTeam(ctx, tx, jamID, CurrentUser(c).ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canEditProduct(team, CurrentUser(c).ID) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.productMutationFailure(c, jamID, "lock finalizing team", err)
		return
	}
	var product ProductView
	var commentary *string
	err = tx.QueryRow(ctx, `
		SELECT id, title, result_url, description, commentary_url, notes, status
		FROM products WHERE team_id=$1 AND jam_id=$2 FOR UPDATE`, team.ID, jamID).Scan(
		&product.ID, &product.Title, &product.ResultURL, &product.Description, &commentary, &product.Notes, &product.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		productRedirectError(c, jamID, "Сначала сохраните карточку продукта.")
		return
	}
	if err != nil {
		a.productMutationFailure(c, jamID, "load product for finalization", err)
		return
	}
	if commentary != nil {
		product.CommentaryURL = *commentary
	}
	if err = validateProductFields(product); err != nil {
		productRedirectError(c, jamID, err.Error())
		return
	}
	err = validateFinalProductTx(ctx, tx, team, product)
	if err != nil {
		a.handleFinalProductError(c, jamID, "validate product finalization", err)
		return
	}
	if !canEditProduct(team, CurrentUser(c).ID) {
		productRedirectError(c, jamID, "Финальная сдача уже закрыта.")
		return
	}
	if _, err = tx.Exec(ctx, `
		UPDATE products SET status='final', finalized_at=COALESCE(finalized_at, now()), updated_at=now()
		WHERE id=$1`, product.ID); err != nil {
		a.productMutationFailure(c, jamID, "finalize product", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.productMutationFailure(c, jamID, "commit product finalization", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/jams/%d/product", jamID))
}

func (a *App) productsList(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	jamTitle, stage, err := a.loadPublishedJamStage(c.Request.Context(), jamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canDiscloseProducts(stage) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.productFailure(c, "load product list jam", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT p.id, p.title, p.result_url, p.description, COALESCE(p.commentary_url, ''),
		       team.id, team.name, theme.phrase,
		       COALESCE((SELECT SUM(bump.bump_count-bump.invalidated_count)::bigint FROM product_bumps bump
		                 WHERE bump.product_id=p.id AND bump.jam_id=p.jam_id), 0)
		FROM products p
		JOIN jams jam ON jam.id=p.jam_id AND jam.visibility='published'
		JOIN teams team ON team.id=p.team_id AND team.jam_id=p.jam_id
		JOIN team_theme_selections selection ON selection.team_id=p.team_id AND selection.jam_id=p.jam_id
		JOIN jam_themes theme ON theme.id=selection.theme_id AND theme.jam_id=p.jam_id
		WHERE p.jam_id=$1 AND p.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL
		          THEN jam.status_override IN ('evaluation', 'voting', 'finished')
		      ELSE clock_timestamp() >= jam.evaluation_starts_at
		  END
		ORDER BY p.finalized_at, p.id`, jamID)
	if err != nil {
		a.productFailure(c, "load public products", err)
		return
	}
	defer rows.Close()
	var products []ProductView
	for rows.Next() {
		var product ProductView
		product.JamID = jamID
		if err = rows.Scan(&product.ID, &product.Title, &product.ResultURL, &product.Description, &product.CommentaryURL, &product.TeamID, &product.TeamName, &product.Theme, &product.BumpCount); err != nil {
			a.productFailure(c, "scan public product", err)
			return
		}
		products = append(products, product)
	}
	if err = rows.Err(); err != nil {
		a.productFailure(c, "iterate public products", err)
		return
	}
	c.HTML(http.StatusOK, "products_list.html", productsListPageData{User: CurrentUser(c), CSRFToken: csrfToken(c), JamID: jamID, JamTitle: jamTitle, Products: products, Stage: stage, BumpsMutable: canMutateBumps(stage)})
}

func (a *App) productDetail(c *gin.Context) {
	productID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	var product ProductView
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(c.Request.Context(), `
		SELECT p.id, p.jam_id, jam.title, p.title, p.result_url, p.description,
		       COALESCE(p.commentary_url, ''), team.id, team.name, theme.phrase,
		       COALESCE((SELECT SUM(bump.bump_count-bump.invalidated_count)::bigint FROM product_bumps bump
		                 WHERE bump.product_id=p.id AND bump.jam_id=p.jam_id), 0),
		       jam.submission_starts_at, jam.evaluation_starts_at, jam.voting_starts_at,
		       jam.finishes_at, jam.status_override
		FROM products p
		JOIN jams jam ON jam.id=p.jam_id AND jam.visibility='published'
		JOIN teams team ON team.id=p.team_id AND team.jam_id=p.jam_id
		JOIN team_theme_selections selection ON selection.team_id=p.team_id AND selection.jam_id=p.jam_id
		JOIN jam_themes theme ON theme.id=selection.theme_id AND theme.jam_id=p.jam_id
		WHERE p.id=$1 AND p.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL
		          THEN jam.status_override IN ('evaluation', 'voting', 'finished')
		      ELSE clock_timestamp() >= jam.evaluation_starts_at
		  END`, productID).Scan(
		&product.ID, &product.JamID, &product.JamTitle, &product.Title, &product.ResultURL,
		&product.Description, &product.CommentaryURL, &product.TeamID, &product.TeamName, &product.Theme, &product.BumpCount,
		&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.productFailure(c, "load public product", err)
		return
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	stage := EffectiveStage(schedule, time.Now())
	if !canDiscloseProducts(stage) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.HTML(http.StatusOK, "product_detail.html", productPageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Product: product, Stage: stage, BumpsMutable: canMutateBumps(stage)})
}

func (a *App) adminProductsPage(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	a.renderAdminProducts(c, jamID, c.Query("error"), http.StatusOK)
}

func (a *App) adminProductUpdate(c *gin.Context) {
	jamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	productID, ok := adminID(c, "productID")
	if !ok {
		return
	}
	product, validationErr := productFromForm(c)
	status := strings.TrimSpace(c.PostForm("status"))
	if status != "draft" && status != "final" {
		a.renderAdminProducts(c, jamID, "Укажите допустимый статус продукта.", http.StatusUnprocessableEntity)
		return
	}
	reason, err := validateReason(c.PostForm("reason"))
	if err != nil {
		a.renderAdminProducts(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if validationErr != nil {
		a.renderAdminProducts(c, jamID, validationErr.Error(), http.StatusUnprocessableEntity)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminProductFailure(c, jamID, "begin admin product update", err)
		return
	}
	defer tx.Rollback(ctx)
	var before ProductView
	var beforeCommentary *string
	var team productTeamRecord
	if err = tx.QueryRow(ctx, `SELECT team_id FROM products WHERE id=$1 AND jam_id=$2`, productID, jamID).Scan(&team.ID); errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	} else if err != nil {
		a.adminProductFailure(c, jamID, "load admin product team", err)
		return
	}
	err = tx.QueryRow(ctx, `
		SELECT team.name, team.captain_user_id,
		       jam.submission_starts_at, jam.evaluation_starts_at, jam.voting_starts_at,
		       jam.finishes_at, jam.status_override
		FROM teams team JOIN jams jam ON jam.id=team.jam_id
		WHERE team.id=$1 AND team.jam_id=$2
		FOR UPDATE OF team FOR SHARE OF jam`, team.ID, jamID).Scan(
		&team.Name, &team.CaptainID, &team.SubmissionStartsAt, &team.EvaluationStartsAt,
		&team.VotingStartsAt, &team.FinishesAt, &team.Override)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.adminProductFailure(c, jamID, "lock admin product", err)
		return
	}
	err = tx.QueryRow(ctx, `
		SELECT id, jam_id, team_id, title, result_url, description, commentary_url, notes, status
		FROM products WHERE id=$1 AND jam_id=$2 AND team_id=$3 FOR UPDATE`, productID, jamID, team.ID).Scan(
		&before.ID, &before.JamID, &before.TeamID, &before.Title, &before.ResultURL,
		&before.Description, &beforeCommentary, &before.Notes, &before.Status)
	if err != nil {
		a.adminProductFailure(c, jamID, "lock product after team", err)
		return
	}
	before.TeamName = team.Name
	if beforeCommentary != nil {
		before.CommentaryURL = *beforeCommentary
	}
	team.JamID = before.JamID
	if before.Status != status && c.PostForm("confirm_status_change") != "yes" {
		a.renderAdminProducts(c, jamID, "Подтвердите изменение статуса продукта.", http.StatusUnprocessableEntity)
		return
	}
	if before.Status == "final" && canDiscloseProducts(productTeamStage(team)) &&
		!productMaterialEqual(before, product, status) && c.PostForm("confirm_post_reveal") != "yes" {
		a.renderAdminProducts(c, jamID, "Подтвердите изменение уже раскрытого продукта.", http.StatusUnprocessableEntity)
		return
	}
	if status == "final" {
		if err = validateFinalProductTx(ctx, tx, team, product); err != nil {
			a.handleAdminFinalProductError(c, jamID, err)
			return
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE products SET title=$3, result_url=$4, description=$5, commentary_url=$6,
			notes=$7, status=$8,
			finalized_at=CASE WHEN $8='final' THEN COALESCE(finalized_at, now()) ELSE NULL END,
			updated_at=now()
		WHERE id=$1 AND jam_id=$2`, productID, jamID, product.Title, product.ResultURL,
		product.Description, nullableProductURL(product.CommentaryURL), product.Notes, status); err != nil {
		a.adminProductFailure(c, jamID, "update product as admin", err)
		return
	}
	product.ID, product.JamID, product.TeamID, product.TeamName, product.Status = productID, jamID, before.TeamID, before.TeamName, status
	if err = insertAdminAudit(ctx, tx, CurrentUser(c), "product.moderate", "product", productID, reason, productAuditData(before), productAuditData(product)); err != nil {
		a.adminProductFailure(c, jamID, "audit product moderation", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminProductFailure(c, jamID, "commit product moderation", err)
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/jams/%d/products", jamID))
}

func (a *App) renderAdminProducts(c *gin.Context, jamID int64, message string, status int) {
	jam, err := a.loadAdminJam(c.Request.Context(), jamID)
	if err != nil {
		a.handleAdminLoadError(c, "load admin product jam", err)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT product.id, product.team_id, team.name, product.title, product.result_url,
		       product.description, COALESCE(product.commentary_url, ''), product.notes, product.status
		FROM products product JOIN teams team ON team.id=product.team_id AND team.jam_id=product.jam_id
		WHERE product.jam_id=$1 ORDER BY lower(team.name), product.id`, jamID)
	if err != nil {
		a.adminProductFailure(c, jamID, "load admin products", err)
		return
	}
	defer rows.Close()
	var products []ProductView
	for rows.Next() {
		var product ProductView
		product.JamID = jamID
		if err = rows.Scan(&product.ID, &product.TeamID, &product.TeamName, &product.Title,
			&product.ResultURL, &product.Description, &product.CommentaryURL, &product.Notes, &product.Status); err != nil {
			a.adminProductFailure(c, jamID, "scan admin product", err)
			return
		}
		products = append(products, product)
	}
	if err = rows.Err(); err != nil {
		a.adminProductFailure(c, jamID, "iterate admin products", err)
		return
	}
	c.HTML(status, "admin_products.html", adminProductsPageData{PageData: PageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: message}, Jam: jam, Products: products})
}

func productAuditData(product ProductView) map[string]any {
	return map[string]any{
		"id": product.ID, "jam_id": product.JamID, "team_id": product.TeamID,
		"title": product.Title, "result_url": product.ResultURL, "description": product.Description,
		"commentary_url": product.CommentaryURL, "notes": product.Notes, "status": product.Status,
	}
}

func productMaterialEqual(before, after ProductView, afterStatus string) bool {
	return before.Title == after.Title && before.ResultURL == after.ResultURL &&
		before.Description == after.Description && before.CommentaryURL == after.CommentaryURL &&
		before.Notes == after.Notes && before.Status == afterStatus
}

func (a *App) handleAdminFinalProductError(c *gin.Context, jamID int64, err error) {
	if errors.Is(err, errProductIncomplete) || errors.Is(err, errProductIneligible) || errors.Is(err, errProductTheme) {
		a.renderAdminProducts(c, jamID, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.adminProductFailure(c, jamID, "validate admin product", err)
}

func (a *App) adminProductFailure(c *gin.Context, jamID int64, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.String(http.StatusInternalServerError, "Не удалось выполнить административное действие с продуктом джема %d.", jamID)
}

func (a *App) loadEditableProductTeam(ctx context.Context, jamID, userID int64) (productTeamRecord, error) {
	var team productTeamRecord
	err := a.pool.QueryRow(ctx, `
		SELECT team.id, team.jam_id, team.name, team.captain_user_id, member.is_product_editor,
		       jam.submission_starts_at, jam.evaluation_starts_at, jam.voting_starts_at,
		       jam.finishes_at, jam.status_override
		FROM team_members member
		JOIN teams team ON team.id=member.team_id AND team.jam_id=member.jam_id
		JOIN jams jam ON jam.id=team.jam_id AND jam.visibility='published'
		WHERE member.jam_id=$1 AND member.user_id=$2`, jamID, userID).Scan(
		&team.ID, &team.JamID, &team.Name, &team.CaptainID, &team.ProductEditor,
		&team.SubmissionStartsAt, &team.EvaluationStartsAt, &team.VotingStartsAt, &team.FinishesAt, &team.Override)
	return team, err
}

func lockProductTeam(ctx context.Context, tx pgx.Tx, jamID, userID int64) (productTeamRecord, error) {
	var team productTeamRecord
	err := tx.QueryRow(ctx, `
		SELECT team.id, team.jam_id, team.name, team.captain_user_id, member.is_product_editor,
		       jam.submission_starts_at, jam.evaluation_starts_at, jam.voting_starts_at,
		       jam.finishes_at, jam.status_override
		FROM team_members member
		JOIN teams team ON team.id=member.team_id AND team.jam_id=member.jam_id
		JOIN jams jam ON jam.id=team.jam_id AND jam.visibility='published'
		WHERE member.jam_id=$1 AND member.user_id=$2
		FOR UPDATE OF team FOR SHARE OF jam`, jamID, userID).Scan(
		&team.ID, &team.JamID, &team.Name, &team.CaptainID, &team.ProductEditor,
		&team.SubmissionStartsAt, &team.EvaluationStartsAt, &team.VotingStartsAt, &team.FinishesAt, &team.Override)
	return team, err
}

func canEditProduct(team productTeamRecord, userID int64) bool {
	return productTeamStage(team) == StageSubmission && (team.CaptainID == userID || team.ProductEditor)
}

func productTeamStage(team productTeamRecord) Stage {
	schedule := Schedule{SubmissionStartsAt: team.SubmissionStartsAt, EvaluationStartsAt: team.EvaluationStartsAt, VotingStartsAt: team.VotingStartsAt, FinishesAt: team.FinishesAt}
	if team.Override != nil {
		stage := Stage(*team.Override)
		schedule.Override = &stage
	}
	return EffectiveStage(schedule, time.Now())
}

func validateFinalProductTx(ctx context.Context, tx pgx.Tx, team productTeamRecord, product ProductView) error {
	if product.Title == "" || product.ResultURL == "" {
		return errProductIncomplete
	}
	var eligible bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_members member
			JOIN questionnaires q ON q.jam_id=member.jam_id
			JOIN questionnaire_responses response ON response.questionnaire_id=q.id
			  AND response.revision=q.current_revision
				AND response.user_id=member.user_id AND response.status='completed'
			WHERE member.team_id=$1 AND member.jam_id=$2
		) OR COALESCE((SELECT allowed FROM team_eligibility_overrides WHERE team_id=$1), false)`, team.ID, team.JamID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return errProductIneligible
	}
	var themeExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_theme_selections selection
			JOIN jam_themes theme ON theme.id=selection.theme_id AND theme.jam_id=selection.jam_id
			WHERE selection.team_id=$1 AND selection.jam_id=$2 AND theme.withdrawn_at IS NULL
		)`, team.ID, team.JamID).Scan(&themeExists); err != nil {
		return err
	}
	if !themeExists {
		return errProductTheme
	}
	return nil
}

func productFromForm(c *gin.Context) (ProductView, error) {
	product := ProductView{
		Title:           strings.TrimSpace(c.PostForm("title")),
		ResultURL:       strings.TrimSpace(c.PostForm("result_url")),
		Description:     strings.TrimSpace(c.PostForm("description")),
		CommentaryURL:   strings.TrimSpace(c.PostForm("commentary_url")),
		Notes:           strings.TrimSpace(c.PostForm("notes")),
		NominationTitle: strings.TrimSpace(c.PostForm("nomination_title")),
	}
	return product, validateProductFields(product)
}

func validateProductFields(product ProductView) error {
	if err := validateNominationTitle(product.NominationTitle, true); err != nil {
		return err
	}
	if utf8.RuneCountInString(product.Title) > productTitleMax || productHasUnsafeControl(product.Title, false) {
		return errors.New("Название должно содержать не более 200 символов без управляющих знаков.")
	}
	if utf8.RuneCountInString(product.Description) > productDescriptionMax || productHasUnsafeControl(product.Description, true) {
		return errors.New("Описание должно содержать не более 5000 символов.")
	}
	if utf8.RuneCountInString(product.Notes) > productNotesMax || productHasUnsafeControl(product.Notes, true) {
		return errors.New("Заметки должны содержать не более 5000 символов.")
	}
	if product.ResultURL != "" {
		if err := validateExternalURL(product.ResultURL); err != nil {
			return errors.New("Ссылка на результат должна быть абсолютным HTTP(S) URL без учётных данных и управляющих знаков.")
		}
	}
	if product.CommentaryURL != "" {
		if err := validateExternalURL(product.CommentaryURL); err != nil {
			return errors.New("Ссылка на комментарий должна быть абсолютным HTTP(S) URL без учётных данных и управляющих знаков.")
		}
	}
	return nil
}

func validateNominationTitle(value string, optional bool) error {
	if value != strings.TrimSpace(value) || (!optional && value == "") ||
		utf8.RuneCountInString(value) > nominationTitleMax || productHasUnsafeControl(value, false) {
		return errors.New("Название номинации должно содержать от 1 до 160 символов без управляющих знаков.")
	}
	return nil
}

func validateExternalURL(value string) error {
	if value == "" || len(value) > productURLMax || value != strings.TrimSpace(value) || strings.Contains(value, `\`) || productHasUnsafeControl(value, false) {
		return errors.New("invalid URL text")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil || productHasUnsafeControl(decoded, false) {
		return errors.New("invalid escaped URL text")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("invalid absolute URL")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("invalid URL scheme")
	}
	if !strings.HasPrefix(strings.ToLower(value), strings.ToLower(parsed.Scheme)+"://") {
		return errors.New("ambiguous URL")
	}
	return nil
}

func productHasUnsafeControl(value string, allowWhitespace bool) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		if !unicode.IsControl(r) {
			return false
		}
		return !allowWhitespace || r != '\n' && r != '\r' && r != '\t'
	}) >= 0
}

func canDiscloseProducts(stage Stage) bool {
	return stage == StageEvaluation || stage == StageVoting || stage == StageFinished
}

func (a *App) loadPublishedJamStage(ctx context.Context, jamID int64) (string, Stage, error) {
	var title string
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(ctx, `
		SELECT title, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override
		FROM jams WHERE id=$1 AND visibility='published'`, jamID).Scan(&title, &schedule.SubmissionStartsAt,
		&schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err != nil {
		return "", "", err
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	return title, EffectiveStage(schedule, time.Now()), nil
}

func nullableProductURL(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *App) renderProductEdit(c *gin.Context, status int, team productTeamRecord, product ProductView, message string) {
	c.HTML(status, "product_edit.html", productPageData{User: CurrentUser(c), CSRFToken: csrfToken(c), Error: message, JamID: team.JamID, TeamID: team.ID, TeamName: team.Name, Product: product})
}

func productRedirectError(c *gin.Context, jamID int64, message string) {
	teamRedirectError(c, fmt.Sprintf("/jams/%d/product", jamID), message)
}

func (a *App) productMutationFailure(c *gin.Context, jamID int64, operation string, err error) {
	a.logger.Error(operation, "error", err)
	productRedirectError(c, jamID, "Не удалось сохранить продукт. Попробуйте позже.")
}

func (a *App) handleFinalProductError(c *gin.Context, jamID int64, operation string, err error) {
	if errors.Is(err, errProductIncomplete) || errors.Is(err, errProductIneligible) || errors.Is(err, errProductTheme) {
		productRedirectError(c, jamID, err.Error())
		return
	}
	a.productMutationFailure(c, jamID, operation, err)
}

func (a *App) productFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.AbortWithStatus(http.StatusInternalServerError)
}

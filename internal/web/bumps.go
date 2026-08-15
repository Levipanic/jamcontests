package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type bumpResponse struct {
	Bumped          bool   `json:"bumped"`
	Count           int64  `json:"count"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	Mutable         bool   `json:"mutable"`
	Error           string `json:"error,omitempty"`
}

type bumpCountResponse struct {
	Count           int64 `json:"count"`
	CooldownSeconds int   `json:"cooldown_seconds"`
	Mutable         bool  `json:"mutable"`
}

func (a *App) registerBumpRoutes(router *gin.Engine) {
	router.GET("/api/products/:id/bumps", a.productBumps)
	router.POST("/api/products/:id/bumps", requireAPIAuth(), a.bumpProduct)
}

func canMutateBumps(stage Stage) bool {
	return stage == StageEvaluation || stage == StageVoting
}

func canDiscloseBumps(stage Stage) bool {
	return canMutateBumps(stage) || stage == StageFinished
}

func (a *App) productBumps(c *gin.Context) {
	productID, ok := a.resolvePublicID(c, "id", "products")
	if !ok {
		bumpNotFound(c)
		return
	}
	var userID any
	if user := CurrentUser(c); user != nil {
		userID = user.ID
	}
	var response bumpCountResponse
	var jamID int64
	var stageValue string
	err := a.pool.QueryRow(c.Request.Context(), `
		SELECT product.jam_id, COALESCE((
			           SELECT SUM(bump.bump_count-bump.invalidated_count)::bigint
		           FROM product_bumps bump
		           WHERE bump.product_id=product.id AND bump.jam_id=product.jam_id
		       ), 0),
		       COALESCE(GREATEST(0, CEIL(EXTRACT(EPOCH FROM (
		           own_bump.last_bumped_at + interval '1 minute' - clock_timestamp()
		       ))))::integer, 0),
		       CASE
		           WHEN jam.status_override IS NOT NULL THEN jam.status_override
		           WHEN clock_timestamp() < jam.submission_starts_at THEN 'upcoming'
		           WHEN clock_timestamp() < jam.evaluation_starts_at THEN 'submission'
		           WHEN clock_timestamp() < jam.voting_starts_at THEN 'evaluation'
		           WHEN clock_timestamp() < jam.finishes_at THEN 'voting'
		           ELSE 'finished'
		       END
		FROM products product
		JOIN jams jam ON jam.id=product.jam_id AND jam.visibility='published'
		LEFT JOIN product_bumps own_bump ON own_bump.product_id=product.id
		    AND own_bump.user_id=$2::bigint
		WHERE product.id=$1 AND product.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL
		          THEN jam.status_override IN ('evaluation', 'voting', 'finished')
		      ELSE clock_timestamp() >= jam.evaluation_starts_at
		  END`, productID, userID).Scan(&jamID, &response.Count, &response.CooldownSeconds, &stageValue)
	if errors.Is(err, pgx.ErrNoRows) {
		bumpNotFound(c)
		return
	}
	if err != nil {
		a.bumpFailure(c, "load product bumps", err)
		return
	}
	response.Mutable = canMutateBumps(Stage(stageValue))
	_, currentStage, recheckErr := a.loadPublishedJamStage(c.Request.Context(), jamID)
	if errors.Is(recheckErr, pgx.ErrNoRows) || recheckErr == nil && currentStage != Stage(stageValue) {
		bumpNotFound(c)
		return
	}
	if recheckErr != nil {
		a.bumpFailure(c, "recheck product bumps", recheckErr)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (a *App) bumpProduct(c *gin.Context) {
	productID, ok := a.resolvePublicID(c, "id", "products")
	if !ok {
		bumpNotFound(c)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.bumpFailure(c, "begin product bump", err)
		return
	}
	defer tx.Rollback(ctx)

	var jamID int64
	var stageValue string
	err = tx.QueryRow(ctx, `
		SELECT product.jam_id,
		       CASE
		           WHEN jam.status_override IS NOT NULL THEN jam.status_override
		           WHEN clock_timestamp() < jam.submission_starts_at THEN 'upcoming'
		           WHEN clock_timestamp() < jam.evaluation_starts_at THEN 'submission'
		           WHEN clock_timestamp() < jam.voting_starts_at THEN 'evaluation'
		           WHEN clock_timestamp() < jam.finishes_at THEN 'voting'
		           ELSE 'finished'
		       END
		FROM products product
		JOIN jams jam ON jam.id=product.jam_id AND jam.visibility='published'
		WHERE product.id=$1 AND product.status='final'
		FOR SHARE OF jam`, productID).Scan(&jamID, &stageValue)
	stage := Stage(stageValue)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canDiscloseBumps(stage) {
		bumpNotFound(c)
		return
	}
	if err != nil {
		a.bumpFailure(c, "lock product bump jam", err)
		return
	}
	if !canMutateBumps(stage) {
		response, stateErr := loadBumpState(ctx, tx, productID, jamID, CurrentUser(c).ID)
		if stateErr != nil {
			a.bumpFailure(c, "load closed product bump state", stateErr)
			return
		}
		if response.Mutable {
			response.Error = "Состояние джема изменилось. Повторите бамп."
		} else {
			response.Error = "Бампы для этого продукта уже закрыты."
		}
		c.JSON(http.StatusConflict, response)
		return
	}

	response := bumpResponse{Bumped: true, Mutable: true}
	err = tx.QueryRow(ctx, `
		INSERT INTO product_bumps (user_id, product_id, jam_id, bump_count, last_bumped_at)
		SELECT $1, product.id, product.jam_id, 1, clock_timestamp()
		FROM products product
		JOIN jams jam ON jam.id=product.jam_id AND jam.visibility='published'
		WHERE product.id=$2 AND product.jam_id=$3 AND product.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL
		          THEN jam.status_override IN ('evaluation', 'voting')
		      ELSE clock_timestamp() >= jam.evaluation_starts_at
		          AND clock_timestamp() < jam.finishes_at
		  END
		ON CONFLICT (user_id, product_id) DO UPDATE SET
		    bump_count=product_bumps.bump_count + 1,
		    last_bumped_at=clock_timestamp()
		WHERE product_bumps.last_bumped_at <= clock_timestamp() - interval '1 minute'
		RETURNING bump_count`, CurrentUser(c).ID, productID, jamID).Scan(new(int64))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.bumpFailure(c, "increment product bump", err)
		return
	}
	bumped := err == nil

	var currentStageValue string
	err = tx.QueryRow(ctx, `
		SELECT CASE
		    WHEN status_override IS NOT NULL THEN status_override
		    WHEN clock_timestamp() < submission_starts_at THEN 'upcoming'
		    WHEN clock_timestamp() < evaluation_starts_at THEN 'submission'
		    WHEN clock_timestamp() < voting_starts_at THEN 'evaluation'
		    WHEN clock_timestamp() < finishes_at THEN 'voting'
		    ELSE 'finished'
		END
		FROM jams WHERE id=$1`, jamID).Scan(&currentStageValue)
	if err != nil {
		a.bumpFailure(c, "recheck product bump stage", err)
		return
	}
	if !canMutateBumps(Stage(currentStageValue)) {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			a.bumpFailure(c, "rollback product bump after stage close", rollbackErr)
			return
		}
		response, stateErr := loadDisclosedBumpState(ctx, a.pool, productID, CurrentUser(c).ID)
		if errors.Is(stateErr, pgx.ErrNoRows) {
			bumpNotFound(c)
			return
		}
		if stateErr != nil {
			a.bumpFailure(c, "load rolled back product bump state", stateErr)
			return
		}
		if response.Mutable {
			response.Error = "Состояние джема изменилось. Повторите бамп."
		} else {
			response.Error = "Бампы для этого продукта уже закрыты."
		}
		c.JSON(http.StatusConflict, response)
		return
	}

	state, err := loadBumpState(ctx, tx, productID, jamID, CurrentUser(c).ID)
	if err != nil {
		a.bumpFailure(c, "load updated product bumps", err)
		return
	}
	response.Count, response.CooldownSeconds = state.Count, state.CooldownSeconds
	response.Bumped = bumped
	if !response.Bumped {
		response.Error = "Повторный бамп будет доступен после окончания паузы."
		c.JSON(http.StatusTooManyRequests, response)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.bumpFailure(c, "commit product bump", err)
		return
	}
	c.JSON(http.StatusOK, response)
}

type bumpQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadBumpState(ctx context.Context, queryer bumpQueryer, productID, jamID, userID int64) (bumpResponse, error) {
	response := bumpResponse{}
	err := queryer.QueryRow(ctx, `
		SELECT COALESCE(SUM(bump_count-invalidated_count), 0)::bigint,
		       COALESCE(GREATEST(0, CEIL(EXTRACT(EPOCH FROM (
		           MAX(last_bumped_at) FILTER (WHERE user_id=$2) + interval '1 minute' - clock_timestamp()
		       ))))::integer, 0)
		FROM product_bumps WHERE product_id=$1 AND jam_id=$3`, productID, userID, jamID).Scan(&response.Count, &response.CooldownSeconds)
	return response, err
}

func loadDisclosedBumpState(ctx context.Context, queryer bumpQueryer, productID, userID int64) (bumpResponse, error) {
	response := bumpResponse{}
	err := queryer.QueryRow(ctx, `
		SELECT COALESCE((SELECT SUM(bump_count-invalidated_count)::bigint FROM product_bumps WHERE product_id=product.id), 0),
		       COALESCE(GREATEST(0, CEIL(EXTRACT(EPOCH FROM (
		           own_bump.last_bumped_at + interval '1 minute' - clock_timestamp()
		       ))))::integer, 0),
		       CASE
		           WHEN jam.status_override IS NOT NULL THEN jam.status_override IN ('evaluation', 'voting')
		           ELSE clock_timestamp() >= jam.evaluation_starts_at AND clock_timestamp() < jam.finishes_at
		       END
		FROM products product
		JOIN jams jam ON jam.id=product.jam_id AND jam.visibility='published'
		LEFT JOIN product_bumps own_bump ON own_bump.product_id=product.id AND own_bump.user_id=$2
		WHERE product.id=$1 AND product.status='final'
		  AND CASE
		      WHEN jam.status_override IS NOT NULL THEN jam.status_override IN ('evaluation', 'voting', 'finished')
		      ELSE clock_timestamp() >= jam.evaluation_starts_at
		  END`, productID, userID).Scan(&response.Count, &response.CooldownSeconds, &response.Mutable)
	return response, err
}

func requireAPIAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c) == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Требуется войти в аккаунт."})
			return
		}
		c.Next()
	}
}

func bumpNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": "Продукт не найден."})
}

func (a *App) bumpFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Не удалось обновить бампы. Попробуйте позже."})
}

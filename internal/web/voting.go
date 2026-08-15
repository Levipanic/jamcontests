package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type voteRequest struct {
	ProductID int64 `json:"product_id"`
}

type voteResponse struct {
	NominationID      int64  `json:"nomination_id,omitempty"`
	SelectedProductID int64  `json:"selected_product_id,omitempty"`
	Error             string `json:"error,omitempty"`
}

type voteCount struct {
	NominationID int64 `json:"nomination_id"`
	ProductID    int64 `json:"product_id"`
	Count        int64 `json:"count"`
}

type voteCountsResponse struct {
	JamID  int64       `json:"jam_id"`
	Counts []voteCount `json:"counts"`
}

func (a *App) registerVotingRoutes(router *gin.Engine) {
	router.GET("/api/jams/:id/vote-counts", a.voteCounts)
	router.POST("/api/jams/:id/nominations/:nominationID/vote", requireAPIAuth(), a.vote)
}

func canVote(stage Stage) bool {
	return stage == StageVoting
}

func (a *App) vote(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		voteUnavailable(c)
		return
	}
	nominationID, ok := teamPositiveID(c.Param("nominationID"))
	if !ok {
		voteUnavailable(c)
		return
	}
	var request voteRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.ProductID <= 0 {
		c.JSON(http.StatusUnprocessableEntity, voteResponse{Error: "Укажите доступный продукт."})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusUnprocessableEntity, voteResponse{Error: "Укажите доступный продукт."})
		return
	}

	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.voteFailure(c, "begin vote", err)
		return
	}
	defer tx.Rollback(ctx)
	var stageValue string
	err = tx.QueryRow(ctx, `
		SELECT CASE
		    WHEN status_override IS NOT NULL THEN status_override
		    WHEN clock_timestamp() < submission_starts_at THEN 'upcoming'
		    WHEN clock_timestamp() < evaluation_starts_at THEN 'submission'
		    WHEN clock_timestamp() < voting_starts_at THEN 'evaluation'
		    WHEN clock_timestamp() < finishes_at THEN 'voting'
		    ELSE 'finished'
		END
		FROM jams WHERE id=$1 AND visibility='published'
		FOR SHARE`, jamID).Scan(&stageValue)
	if errors.Is(err, pgx.ErrNoRows) {
		voteUnavailable(c)
		return
	}
	if err != nil {
		a.voteFailure(c, "lock vote jam", err)
		return
	}
	if !canVote(Stage(stageValue)) {
		c.JSON(http.StatusConflict, voteResponse{Error: "Голосование сейчас недоступно."})
		return
	}

	var productTeamID int64
	err = tx.QueryRow(ctx, `
		SELECT product.team_id
		FROM nominations nomination
		JOIN products product ON product.id=$3 AND product.jam_id=nomination.jam_id
		JOIN teams product_team ON product_team.id=product.team_id AND product_team.jam_id=product.jam_id
		JOIN products nomination_product
		  ON nomination_product.id=CASE WHEN nomination.kind='team' THEN nomination.product_id ELSE product.id END
		 AND nomination_product.jam_id=nomination.jam_id
		 AND nomination_product.status='final'
		WHERE nomination.id=$2 AND nomination.jam_id=$1
		  AND nomination.withdrawn_at IS NULL AND product.status='final'
		FOR SHARE OF nomination, product, product_team, nomination_product`, jamID, nominationID, request.ProductID).Scan(&productTeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		voteUnavailable(c)
		return
	}
	if err != nil {
		a.voteFailure(c, "validate vote entities", err)
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(format('vote-membership:%s:%s', $1::bigint, $2::bigint), 0))`, jamID, CurrentUser(c).ID); err != nil {
		a.voteFailure(c, "lock vote membership", err)
		return
	}

	var ownProduct bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_members
			WHERE jam_id=$1 AND team_id=$2 AND user_id=$3
		)`, jamID, productTeamID, CurrentUser(c).ID).Scan(&ownProduct); err != nil {
		a.voteFailure(c, "check vote membership", err)
		return
	}
	if ownProduct {
		c.JSON(http.StatusUnprocessableEntity, voteResponse{Error: "Нельзя голосовать за продукт своей текущей команды."})
		return
	}

	var response voteResponse
	err = tx.QueryRow(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, nomination_id) WHERE invalidated_at IS NULL DO UPDATE SET
		    product_id=EXCLUDED.product_id,
		    updated_at=clock_timestamp()
		RETURNING nomination_id, product_id`, CurrentUser(c).ID, nominationID, request.ProductID, jamID).Scan(&response.NominationID, &response.SelectedProductID)
	if err != nil {
		a.voteFailure(c, "save vote", err)
		return
	}

	var stillVoting bool
	if err = tx.QueryRow(ctx, `
		SELECT CASE
		    WHEN status_override IS NOT NULL THEN status_override='voting'
		    ELSE clock_timestamp() >= voting_starts_at AND clock_timestamp() < finishes_at
		END
		FROM jams WHERE id=$1 AND visibility='published'`, jamID).Scan(&stillVoting); err != nil {
		a.voteFailure(c, "recheck vote deadline", err)
		return
	}
	if !stillVoting {
		c.JSON(http.StatusConflict, voteResponse{Error: "Голосование сейчас недоступно."})
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.voteFailure(c, "commit vote", err)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (a *App) voteCounts(c *gin.Context) {
	jamID, ok := teamPositiveID(c.Param("id"))
	if !ok {
		voteCountsUnavailable(c)
		return
	}
	ctx := c.Request.Context()
	open, err := a.voteCountsOpen(ctx, jamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !open {
		voteCountsUnavailable(c)
		return
	}
	if err != nil {
		a.voteFailure(c, "check vote counts stage", err)
		return
	}

	rows, err := a.pool.Query(ctx, `
		WITH public_nominations AS (
			SELECT nomination.id
			FROM nominations nomination
			WHERE nomination.jam_id=$1 AND nomination.withdrawn_at IS NULL
			  AND (nomination.kind='curator' OR EXISTS (
			      SELECT 1 FROM products author_product
			      WHERE author_product.id=nomination.product_id
			        AND author_product.jam_id=nomination.jam_id
			        AND author_product.status='final'
			  ))
		), public_products AS (
			SELECT product.id FROM products product
			WHERE product.jam_id=$1 AND product.status='final'
		)
		SELECT nomination.id, product.id, count(vote.user_id)::bigint
		FROM public_nominations nomination
		CROSS JOIN public_products product
		LEFT JOIN nomination_votes vote
		  ON vote.jam_id=$1 AND vote.nomination_id=nomination.id AND vote.product_id=product.id
		 AND vote.invalidated_at IS NULL
		GROUP BY nomination.id, product.id
		ORDER BY nomination.id, product.id`, jamID)
	if err != nil {
		a.voteFailure(c, "load vote counts", err)
		return
	}
	defer rows.Close()
	response := voteCountsResponse{JamID: jamID, Counts: []voteCount{}}
	for rows.Next() {
		var count voteCount
		if err = rows.Scan(&count.NominationID, &count.ProductID, &count.Count); err != nil {
			a.voteFailure(c, "scan vote counts", err)
			return
		}
		response.Counts = append(response.Counts, count)
	}
	if err = rows.Err(); err != nil {
		a.voteFailure(c, "iterate vote counts", err)
		return
	}
	open, err = a.voteCountsOpen(ctx, jamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !open {
		voteCountsUnavailable(c)
		return
	}
	if err != nil {
		a.voteFailure(c, "recheck vote counts stage", err)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (a *App) voteCountsOpen(ctx context.Context, jamID int64) (bool, error) {
	var open bool
	err := a.pool.QueryRow(ctx, `
		SELECT CASE
		    WHEN status_override IS NOT NULL THEN status_override='voting'
		    ELSE clock_timestamp() >= voting_starts_at AND clock_timestamp() < finishes_at
		END
		FROM jams WHERE id=$1 AND visibility='published'`, jamID).Scan(&open)
	return open, err
}

func voteUnavailable(c *gin.Context) {
	c.JSON(http.StatusNotFound, voteResponse{Error: "Номинация или продукт недоступны для голосования."})
}

func voteCountsUnavailable(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": "Счётчики голосов сейчас недоступны."})
}

func (a *App) voteFailure(c *gin.Context, operation string, err error) {
	a.logger.Error(operation, "error", err)
	c.JSON(http.StatusInternalServerError, voteResponse{Error: "Не удалось обработать голос. Попробуйте позже."})
}

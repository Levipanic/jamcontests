package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const publicIDLength = 18

// newPublicID generates a cryptographically random opaque identifier. The
// database unique constraint protects against the astronomically unlikely
// collision; callers do not need a retry loop.
func newPublicID() (string, error) {
	buffer := make([]byte, 9)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validPublicID(value string) bool {
	if len(value) != publicIDLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

var publicIDLookup = map[string]string{
	"jams":        `SELECT id FROM jams WHERE public_id = $1`,
	"teams":       `SELECT id FROM teams WHERE public_id = $1`,
	"products":    `SELECT id FROM products WHERE public_id = $1`,
	"nominations": `SELECT id FROM nominations WHERE public_id = $1`,
}

var internalIDLookup = map[string]string{
	"jams":        `SELECT public_id FROM jams WHERE id = $1`,
	"teams":       `SELECT public_id FROM teams WHERE id = $1`,
	"products":    `SELECT public_id FROM products WHERE id = $1`,
	"nominations": `SELECT public_id FROM nominations WHERE id = $1`,
}

// resolvePublicID maps an opaque route identifier to the internal row id.
// Invalid identifiers and missing rows are indistinguishable: both produce a
// neutral 404 so guesses cannot probe existence.
func (a *App) resolvePublicID(c *gin.Context, param, entity string) (int64, bool) {
	raw := c.Param(param)
	if !validPublicID(raw) {
		a.writeError(c, http.StatusNotFound, "")
		return 0, false
	}
	var id int64
	err := a.pool.QueryRow(c.Request.Context(), publicIDLookup[entity], raw).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		a.writeError(c, http.StatusNotFound, "")
		return 0, false
	}
	if err != nil {
		a.logger.Error("resolve public identifier", "error", err, "entity", entity)
		a.writeError(c, http.StatusInternalServerError, "")
		return 0, false
	}
	return id, true
}

// resolvePublicIDValue maps an opaque identifier carried in a request body to
// the internal row id, without touching the response. Missing and invalid
// identifiers are indistinguishable so guesses cannot probe existence.
func (a *App) resolvePublicIDValue(ctx context.Context, entity, value string) (int64, bool) {
	if !validPublicID(value) {
		return 0, false
	}
	var id int64
	err := a.pool.QueryRow(ctx, publicIDLookup[entity], value).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		a.logger.Error("resolve public identifier value", "error", err, "entity", entity)
		return 0, false
	}
	return id, true
}

// publicIDOf returns the public identifier for an internal row id, for use in
// redirects and view construction after the row is known to exist.
func (a *App) publicIDOf(ctx context.Context, entity string, id int64) (string, error) {
	var value string
	err := a.pool.QueryRow(ctx, internalIDLookup[entity], id).Scan(&value)
	return value, err
}

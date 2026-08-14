package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const currentUserKey = "current_user"

type User struct {
	ID       int64
	Username string
	Email    *string
	Role     string
}

func (a *App) currentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(a.config.SessionCookie)
		if err == nil {
			if token, decodeErr := base64.RawURLEncoding.DecodeString(raw); decodeErr == nil && len(token) == 32 {
				hash := sha256.Sum256(token)
				var user User
				err = a.pool.QueryRow(c.Request.Context(), `
					SELECT u.id, u.username, u.email, u.role
					FROM sessions s JOIN users u ON u.id = s.user_id
					WHERE s.token_hash = $1 AND s.expires_at > now()`, hash[:]).Scan(&user.ID, &user.Username, &user.Email, &user.Role)
				if err == nil {
					c.Set(currentUserKey, &user)
				} else if err != pgx.ErrNoRows {
					a.logger.Error("load session user", "error", err)
				}
			}
		}
		c.Next()
	}
}

func CurrentUser(c *gin.Context) *User {
	value, exists := c.Get(currentUserKey)
	if !exists {
		return nil
	}
	user, _ := value.(*User)
	return user
}

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c) == nil {
			next := safeNext(c.Request.URL.RequestURI())
			c.Redirect(http.StatusSeeOther, "/login?next="+urlQueryEscape(next))
			c.Abort()
			return
		}
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if user.Role != "admin" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func (a *App) createSession(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, userID int64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	_, err := db.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash[:], userID, time.Now().Add(a.config.SessionTTL))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *App) setSessionCookie(c *gin.Context, token string) {
	maxAge := int(a.config.SessionTTL / time.Second)
	http.SetCookie(c.Writer, &http.Cookie{Name: a.config.SessionCookie, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: a.config.Production(), SameSite: http.SameSiteLaxMode})
}

func (a *App) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: a.config.SessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: a.config.Production(), SameSite: http.SameSiteLaxMode})
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

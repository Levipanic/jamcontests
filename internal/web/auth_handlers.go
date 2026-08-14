package web

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Levipanic/jamcontests/internal/auth"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (a *App) register(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	email, validationErr := validateIdentity(username, c.PostForm("email"))
	password := c.PostForm("password")
	if validationErr == nil && (len(password) < 10 || len(password) > 1024) {
		validationErr = errors.New("Пароль должен содержать от 10 до 1024 байт.")
	}
	if validationErr != nil {
		a.renderAuthError(c, http.StatusUnprocessableEntity, "register", validationErr.Error())
		return
	}
	if !a.allowAuthAttempt(c, "register", "", 8, 10*time.Minute) {
		a.renderAuthError(c, http.StatusTooManyRequests, "register", "Слишком много попыток регистрации. Повторите позже.")
		return
	}
	if !a.acquirePasswordWork() {
		c.Header("Retry-After", "5")
		a.renderAuthError(c, http.StatusTooManyRequests, "register", "Сервер занят проверкой паролей. Повторите через несколько секунд.")
		return
	}
	passwordHash, err := auth.HashPassword(password)
	a.releasePasswordWork()
	if err != nil {
		a.logger.Error("hash registration password")
		a.renderAuthError(c, http.StatusInternalServerError, "register", "Не удалось создать аккаунт. Попробуйте позже.")
		return
	}

	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		a.logger.Error("begin registration", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "register", "Не удалось создать аккаунт. Попробуйте позже.")
		return
	}
	defer tx.Rollback(c.Request.Context())
	var userID int64
	err = tx.QueryRow(c.Request.Context(), `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, $3, 'user') RETURNING id`, username, email, passwordHash).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			a.renderAuthError(c, http.StatusConflict, "register", "Имя пользователя или email уже заняты.")
			return
		}
		a.logger.Error("insert registered user", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "register", "Не удалось создать аккаунт. Попробуйте позже.")
		return
	}
	token, err := a.createSession(c.Request.Context(), tx, userID)
	if err != nil {
		a.logger.Error("create registration session", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "register", "Не удалось создать аккаунт. Попробуйте позже.")
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		a.logger.Error("commit registration", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "register", "Не удалось создать аккаунт. Попробуйте позже.")
		return
	}
	a.setSessionCookie(c, token)
	c.Redirect(http.StatusSeeOther, safeNext(c.PostForm("next")))
}

func (a *App) login(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	if !a.allowAuthAttempt(c, "login", username, 12, 5*time.Minute) {
		a.renderAuthError(c, http.StatusTooManyRequests, "login", "Слишком много попыток входа. Повторите позже.")
		return
	}
	var userID int64
	var passwordHash string
	err := pgx.ErrNoRows
	credentialsWellFormed := utf8.RuneCountInString(username) >= 3 && utf8.RuneCountInString(username) <= 40 && !hasControl(username) && len(password) >= 10 && len(password) <= 1024
	if credentialsWellFormed {
		err = a.pool.QueryRow(c.Request.Context(), `SELECT id, password_hash FROM users WHERE lower(username) = lower($1)`, username).Scan(&userID, &passwordHash)
	}
	if err != nil && err != pgx.ErrNoRows {
		a.logger.Error("load login user", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "login", "Не удалось выполнить вход. Попробуйте позже.")
		return
	}
	if !a.acquirePasswordWork() {
		c.Header("Retry-After", "5")
		a.renderAuthError(c, http.StatusTooManyRequests, "login", "Сервер занят проверкой паролей. Повторите через несколько секунд.")
		return
	}
	passwordMatches := err != pgx.ErrNoRows && auth.CheckPassword(passwordHash, password)
	if err == pgx.ErrNoRows {
		candidate := password
		if len(candidate) > 1024 {
			candidate = ""
		}
		auth.CheckPassword(a.dummyHash, candidate)
	}
	a.releasePasswordWork()
	if !passwordMatches {
		a.renderAuthError(c, http.StatusUnauthorized, "login", "Неверное имя пользователя или пароль.")
		return
	}
	token, err := a.createSession(c.Request.Context(), a.pool, userID)
	if err != nil {
		a.logger.Error("create login session", "error", err)
		a.renderAuthError(c, http.StatusInternalServerError, "login", "Не удалось выполнить вход. Попробуйте позже.")
		return
	}
	a.setSessionCookie(c, token)
	c.Redirect(http.StatusSeeOther, safeNext(c.PostForm("next")))
}

func (a *App) logout(c *gin.Context) {
	if raw, err := c.Cookie(a.config.SessionCookie); err == nil {
		if token, decodeErr := base64.RawURLEncoding.DecodeString(raw); decodeErr == nil && len(token) == 32 {
			hash := sha256.Sum256(token)
			if _, deleteErr := a.pool.Exec(c.Request.Context(), `DELETE FROM sessions WHERE token_hash = $1`, hash[:]); deleteErr != nil {
				a.logger.Error("delete session", "error", deleteErr)
				a.render(c, http.StatusInternalServerError, "home.html", PageData{Error: "Не удалось выполнить выход. Попробуйте позже."})
				return
			}
		}
	}
	a.clearSessionCookie(c)
	c.Redirect(http.StatusSeeOther, "/")
}

func (a *App) renderAuthError(c *gin.Context, status int, mode, message string) {
	data := PageData{Error: message, AuthOpen: true, AuthMode: mode, Next: safeNext(c.PostForm("next")), AuthUsername: strings.TrimSpace(c.PostForm("username"))}
	if mode == "register" {
		data.AuthEmail = strings.TrimSpace(c.PostForm("email"))
	}
	if err := a.populateHome(c, &data); err != nil {
		a.logger.Error("load home after authentication error", "error", err)
	}
	a.render(c, status, "home.html", data)
}

func validateIdentity(username, rawEmail string) (*string, error) {
	if utf8.RuneCountInString(username) < 3 || utf8.RuneCountInString(username) > 40 || hasControl(username) {
		return nil, errors.New("Имя пользователя должно содержать от 3 до 40 символов без управляющих знаков.")
	}
	email := strings.TrimSpace(rawEmail)
	if email == "" {
		return nil, nil
	}
	if len(email) > 254 || hasControl(email) {
		return nil, errors.New("Укажите корректный email длиной не более 254 символов.")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return nil, errors.New("Укажите корректный email длиной не более 254 символов.")
	}
	return &email, nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func safeNext(value string) string {
	if value == "" {
		return "/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") || hasControl(value) {
		return "/"
	}
	return parsed.RequestURI()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

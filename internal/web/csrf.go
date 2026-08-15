package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	csrfContextKey = "csrf_token"
	csrfCookieName = "jamcontests_csrf"
)

func (a *App) csrf() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieName := a.csrfCookieName()
		binding := a.csrfSessionBinding(c)
		token, err := c.Cookie(cookieName)
		if err != nil || !a.validCSRFSignature(token, binding) {
			token, err = a.newCSRFToken(binding)
			if err != nil {
				a.logger.Error("generate CSRF token")
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.config.Production(), SameSite: http.SameSiteLaxMode})
		}
		c.Set(csrfContextKey, token)

		if isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}
		supplied := c.GetHeader("X-CSRF-Token")
		if supplied == "" {
			supplied = c.PostForm("csrf_token")
		}
		if !a.validCSRFSignature(supplied, binding) || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
			a.writeError(c, http.StatusForbidden, "Недействительный CSRF-токен.")
			return
		}
		c.Next()
	}
}

func (a *App) newCSRFToken(binding ...string) (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, a.config.CSRFSecret)
	mac.Write([]byte(encoded))
	mac.Write([]byte{0})
	mac.Write([]byte(firstString(binding)))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *App) validCSRFSignature(token string, binding ...string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, a.config.CSRFSecret)
	mac.Write([]byte(parts[0]))
	mac.Write([]byte{0})
	mac.Write([]byte(firstString(binding)))
	return hmac.Equal(signature, mac.Sum(nil))
}

func (a *App) csrfSessionBinding(c *gin.Context) string {
	raw, err := c.Cookie(a.config.SessionCookie)
	if err != nil || raw == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (a *App) csrfCookieName() string {
	if a.config.Production() {
		return "__Host-jamcontests_csrf"
	}
	return csrfCookieName
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func csrfToken(c *gin.Context) string {
	value, _ := c.Get(csrfContextKey)
	token, _ := value.(string)
	return token
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || method == http.MethodTrace
}

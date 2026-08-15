package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type authAttempt struct {
	count   int
	resetAt time.Time
}

type authAttemptLimiter struct {
	mu       sync.Mutex
	attempts map[string]authAttempt
}

func newAuthAttemptLimiter() *authAttemptLimiter {
	return &authAttemptLimiter{attempts: make(map[string]authAttempt)}
}

func (l *authAttemptLimiter) allow(key string, maximum int, window time.Duration) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[key]
	if attempt.resetAt.IsZero() || !now.Before(attempt.resetAt) {
		delete(l.attempts, key)
		if len(l.attempts) >= 5000 {
			for existingKey, existing := range l.attempts {
				if !now.Before(existing.resetAt) {
					delete(l.attempts, existingKey)
				}
			}
			if len(l.attempts) >= 5000 {
				return false, window
			}
		}
		l.attempts[key] = authAttempt{count: 1, resetAt: now.Add(window)}
		return true, 0
	}
	if attempt.count >= maximum {
		return false, time.Until(attempt.resetAt)
	}
	attempt.count++
	l.attempts[key] = attempt
	return true, 0
}

func (a *App) allowAuthAttempt(c *gin.Context, action, username string, maximum int, window time.Duration) bool {
	ipKey := action + "|ip|" + requestIP(c.Request)
	ipMaximum := maximum
	if username != "" {
		ipMaximum = maximum * 3
	}
	allowed, retry := a.authLimit.allow(ipKey, ipMaximum, window)
	if allowed && username != "" {
		identityHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
		identityKey := action + "|identity|" + hex.EncodeToString(identityHash[:])
		allowed, retry = a.authLimit.allow(identityKey, maximum, window)
	}
	if allowed {
		return true
	}
	seconds := int(retry.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	return false
}

func (a *App) acquirePasswordWork() bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case a.passwordWork <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (a *App) releasePasswordWork() {
	<-a.passwordWork
}

func requestIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

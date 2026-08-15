package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSafeErrorResponsesNegotiateHTMLAndJSON(t *testing.T) {
	router := New(publicTestConfig(t), nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	router.GET("/empty-error", func(c *gin.Context) { c.AbortWithStatus(http.StatusNotFound) })
	router.GET("/empty-redirect", func(c *gin.Context) { c.Redirect(http.StatusSeeOther, "/target") })

	htmlResponse := httptest.NewRecorder()
	router.ServeHTTP(htmlResponse, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if htmlResponse.Code != http.StatusNotFound || !strings.Contains(htmlResponse.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("HTML error status=%d content-type=%q", htmlResponse.Code, htmlResponse.Header().Get("Content-Type"))
	}
	if !strings.Contains(htmlResponse.Body.String(), "Дело недоступно") || strings.Contains(htmlResponse.Body.String(), "/home/") {
		t.Fatalf("unsafe or unreadable HTML error: %s", htmlResponse.Body.String())
	}
	if htmlResponse.Header().Get("X-Request-ID") == "" || !strings.Contains(htmlResponse.Body.String(), htmlResponse.Header().Get("X-Request-ID")) {
		t.Fatal("HTML error does not expose its correlation ID")
	}
	emptyResponse := httptest.NewRecorder()
	router.ServeHTTP(emptyResponse, httptest.NewRequest(http.MethodGet, "/empty-error", nil))
	if emptyResponse.Code != http.StatusNotFound || !strings.Contains(emptyResponse.Header().Get("Content-Type"), "text/html") || !strings.Contains(emptyResponse.Body.String(), "Дело недоступно") {
		t.Fatalf("empty handler error was not safely rendered: status=%d content-type=%q body=%s", emptyResponse.Code, emptyResponse.Header().Get("Content-Type"), emptyResponse.Body.String())
	}
	redirectResponse := httptest.NewRecorder()
	router.ServeHTTP(redirectResponse, httptest.NewRequest(http.MethodGet, "/empty-redirect", nil))
	if redirectResponse.Code != http.StatusSeeOther || redirectResponse.Header().Get("Location") != "/target" {
		t.Fatalf("redirect status was not preserved: status=%d location=%q", redirectResponse.Code, redirectResponse.Header().Get("Location"))
	}

	jsonResponse := httptest.NewRecorder()
	router.ServeHTTP(jsonResponse, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if jsonResponse.Code != http.StatusNotFound || !strings.Contains(jsonResponse.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("JSON error status=%d content-type=%q", jsonResponse.Code, jsonResponse.Header().Get("Content-Type"))
	}
	var body map[string]string
	if err := json.Unmarshal(jsonResponse.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["request_id"] == "" || body["request_id"] != jsonResponse.Header().Get("X-Request-ID") || body["error"] == "" {
		t.Fatalf("incomplete JSON error: %#v", body)
	}
}

func TestRecoveryDoesNotLogSecretsAndLogsCompletion(t *testing.T) {
	var logs bytes.Buffer
	cfg := publicTestConfig(t)
	cfg.Environment = "production"
	router := New(cfg, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	router.GET("/panic-test", func(*gin.Context) { panic("panic-secret") })

	request := httptest.NewRequest(http.MethodGet, "/panic-test", nil)
	request.Header.Set("Cookie", "attacker_cookie=cookie-secret")
	request.Header.Set("X-CSRF-Token", "csrf-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("panic response status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	for _, secret := range []string{"panic-secret", "cookie-secret", "csrf-secret"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(response.Body.String(), secret) {
			t.Fatalf("panic output contains secret %q", secret)
		}
	}
	requestID := response.Header().Get("X-Request-ID")
	if requestID == "" || strings.Count(logs.String(), requestID) < 2 || !strings.Contains(logs.String(), "status=500") || !strings.Contains(logs.String(), "request panic") {
		t.Fatalf("panic logs lack correlated error and completion: %s", logs.String())
	}
}

func TestProductionCSRFCookieIsHostOnlyAndSecure(t *testing.T) {
	cfg := publicTestConfig(t)
	cfg.Environment = "production"
	router := New(cfg, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	cookie := responseCookie(t, response.Result(), "__Host-jamcontests_csrf")
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe production CSRF cookie: %+v", cookie)
	}
}

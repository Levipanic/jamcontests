package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Levipanic/jamcontests/internal/auth"
	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fallbackTemplates = `
{{define "home.html"}}<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="csrf-token" content="{{.CSRFToken}}"><title>Jam Contests</title></head><body><main><h1>Jam Contests</h1>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{if .Jam}}<section><h2>{{.Jam.Title}}</h2><p>{{.Jam.Description}}</p><p>Стадия: {{.Jam.Stage}}</p></section>{{else}}<p>Сейчас нет активного джема.</p>{{end}}{{if .User}}<section><h2>Профиль</h2><p>{{.User.Username}}</p><form method="post" action="/logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button type="submit">Выйти</button></form></section>{{else}}<section aria-label="Вход и регистрация"><h2>Вход</h2><form method="post" action="/login"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="next" value="{{.Next}}"><label>Имя пользователя <input name="username" required autocomplete="username"></label><label>Пароль <input name="password" type="password" required autocomplete="current-password"></label><button type="submit">Войти</button></form><h2>Регистрация</h2><form method="post" action="/register"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="next" value="{{.Next}}"><label>Имя пользователя <input name="username" required autocomplete="username"></label><label>Email <input name="email" type="email" autocomplete="email"></label><label>Пароль <input name="password" type="password" required autocomplete="new-password"></label><button type="submit">Создать аккаунт</button></form></section>{{end}}</main></body></html>{{end}}
{{define "admin.html"}}<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="csrf-token" content="{{.CSRFToken}}"><title>Администрирование</title></head><body><main><h1>Администрирование</h1><p>Панель подготовлена для следующих разделов.</p></main></body></html>{{end}}
{{define "error.html"}}<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Status}} | Jam Contests</title></head><body><main><h1>{{.Status}}</h1><p>{{.Message}}</p><p>Номер обращения: {{.RequestID}}</p><a href="/">На главную</a></main></body></html>{{end}}
`

const requestIDContextKey = "request_id"

type App struct {
	config       config.Config
	pool         *pgxpool.Pool
	logger       *slog.Logger
	templates    *template.Template
	dummyHash    string
	authLimit    *authAttemptLimiter
	passwordWork chan struct{}
}

type PageData struct {
	User             *User
	CSRFToken        string
	Error            string
	Ok               string
	AuthOpen         bool
	AuthMode         string
	AuthUsername     string
	AuthEmail        string
	Next             string
	AutoOpenAuth     bool
	Jam              *JamView
	Teams            []HomeTeamView
	Profile          *ProfileView
	ProfileError     string
	Themes           []ThemeView
	SelectedTheme    *ThemeView
	ThemeConfigError bool
}

type JamView struct {
	ID               int64
	PublicID         string
	Title            string
	Description      string
	Rules            string
	Stage            Stage
	StageIndex       int
	NextStageAt      *time.Time
	NextStageRFC3339 string
	MaxTeamSize      int
	Dates            []JamDateView
}

// New constructs the HTTP router. Invalid template syntax is a startup error.
func New(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) *gin.Engine {
	if logger == nil {
		logger = slog.Default()
	}
	tmpl, err := loadTemplates(cfg.TemplatesDir)
	if err != nil {
		panic(err)
	}
	dummyHash, err := auth.HashPassword("invalid-password-placeholder")
	if err != nil {
		panic(err)
	}

	app := &App{
		config: cfg, pool: pool, logger: logger, templates: tmpl, dummyHash: dummyHash,
		authLimit: newAuthAttemptLimiter(), passwordWork: make(chan struct{}, 2),
	}
	if cfg.Production() {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.HandleMethodNotAllowed = true
	if err := router.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		panic(err)
	}
	router.MaxMultipartMemory = cfg.MaxAvatarBytes
	router.SetHTMLTemplate(tmpl)
	router.Use(app.requestID(), app.requestLog(), app.recovery(), app.securityHeaders(), app.cachePolicy(), app.errorPages(), app.limitRequestBody(), app.csrf(), app.currentUser())

	router.Static("/static", cfg.StaticDir)
	router.GET("/avatars/:name", app.avatar)
	router.GET("/health", app.health)
	router.GET("/", app.home)
	router.GET("/jams/:id", app.jamDetail)
	router.GET("/archive", app.archive)
	router.GET("/login", app.authPage("login"))
	router.GET("/register", app.authPage("register"))
	router.POST("/register", app.register)
	router.POST("/login", app.login)
	router.POST("/logout", app.logout)
	router.POST("/profile", RequireAuth(), app.updateProfile)
	app.registerJamAdminRoutes(router)
	app.registerAdminControlRoutes(router)
	app.registerTeamRoutes(router)
	app.registerQuestionnaireRoutes(router)
	app.registerThemeRoutes(router)
	app.registerProductRoutes(router)
	app.registerNominationRoutes(router)
	app.registerVotingRoutes(router)
	app.registerBumpRoutes(router)
	app.registerAdminInterventionRoutes(router)
	router.NoRoute(func(c *gin.Context) { app.writeError(c, http.StatusNotFound, "") })
	router.NoMethod(func(c *gin.Context) { app.writeError(c, http.StatusNotFound, "") })

	return router
}

func loadTemplates(root string) (*template.Template, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".html" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return template.New("root").Funcs(templateFuncs()).Parse(fallbackTemplates)
	}
	return template.New("root").Funcs(templateFuncs()).ParseFiles(paths...)
}

func (a *App) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				a.logger.Error("request panic", "request_id", requestID(c), "method", c.Request.Method, "route", requestRoute(c))
				if !c.Writer.Written() {
					a.writeError(c, http.StatusInternalServerError, "")
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}

func (a *App) requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		value := make([]byte, 16)
		if _, err := rand.Read(value); err != nil {
			a.logger.Error("generate request id")
			value = []byte(time.Now().UTC().Format("20060102150405.000000000"))
		}
		id := hex.EncodeToString(value)
		c.Set(requestIDContextKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func (a *App) requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		a.logger.Info("http request", "request_id", requestID(c), "method", c.Request.Method, "route", requestRoute(c), "status", c.Writer.Status(), "duration", time.Since(started))
	}
}

func (a *App) errorPages() gin.HandlerFunc {
	return func(c *gin.Context) {
		writer := &deferredStatusWriter{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = writer
		c.Next()
		if writer.Status() >= http.StatusBadRequest && writer.Size() <= 0 {
			a.writeError(c, writer.Status(), "")
		}
		if !writer.Written() {
			writer.WriteHeaderNow()
		}
	}
}

type deferredStatusWriter struct {
	gin.ResponseWriter
	status  int
	written bool
}

func (writer *deferredStatusWriter) WriteHeader(status int) {
	if !writer.written && status > 0 {
		writer.status = status
	}
}

func (writer *deferredStatusWriter) WriteHeaderNow() {
	if writer.written {
		return
	}
	writer.ResponseWriter.WriteHeader(writer.status)
	writer.ResponseWriter.WriteHeaderNow()
	writer.written = true
}

func (writer *deferredStatusWriter) Write(data []byte) (int, error) {
	writer.WriteHeaderNow()
	return writer.ResponseWriter.Write(data)
}

func (writer *deferredStatusWriter) WriteString(data string) (int, error) {
	writer.WriteHeaderNow()
	return writer.ResponseWriter.WriteString(data)
}

func (writer *deferredStatusWriter) Flush() {
	writer.WriteHeaderNow()
	writer.ResponseWriter.Flush()
}

func (writer *deferredStatusWriter) Status() int   { return writer.status }
func (writer *deferredStatusWriter) Written() bool { return writer.written }

type errorPageData struct {
	Status    int
	Message   string
	RequestID string
}

func (a *App) writeError(c *gin.Context, status int, message string) {
	if message == "" {
		message = safeErrorMessage(status)
	}
	c.Abort()
	if wantsJSON(c) {
		c.JSON(status, gin.H{"error": message, "request_id": requestID(c)})
		return
	}
	c.HTML(status, "error.html", errorPageData{Status: status, Message: message, RequestID: requestID(c)})
}

func safeErrorMessage(status int) string {
	switch status {
	case http.StatusForbidden:
		return "Доступ к этому материалу запрещён."
	case http.StatusConflict:
		return "Действие конфликтует с текущим состоянием. Обновите страницу и попробуйте снова."
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "Проверьте введённые данные и попробуйте снова."
	case http.StatusInternalServerError:
		return "Не удалось обработать запрос. Попробуйте позже."
	default:
		return "Запрошенный материал не найден или недоступен."
	}
}

func wantsJSON(c *gin.Context) bool {
	return strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.Contains(c.GetHeader("Accept"), "application/json") || c.ContentType() == "application/json"
}

func requestID(c *gin.Context) string {
	value, _ := c.Get(requestIDContextKey)
	id, _ := value.(string)
	return id
}

func (a *App) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}

func (a *App) cachePolicy() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/static/") {
			c.Next()
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.Header("Pragma", "no-cache")
		c.Header("Vary", "Cookie")
		c.Next()
	}
}

func (a *App) limitRequestBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isSafeMethod(c.Request.Method) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, a.config.MaxAvatarBytes+1024*1024)
		}
		c.Next()
	}
}

func (a *App) avatar(c *gin.Context) {
	name := c.Param("name")
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	path := filepath.Join(a.config.AvatarDir, name)
	user := CurrentUser(c)
	isAdmin := user != nil && user.Role == "admin"
	var referenced bool
	if err := a.pool.QueryRow(c.Request.Context(), `
		SELECT EXISTS (
			SELECT 1
			FROM teams t
			JOIN jams j ON j.id = t.jam_id
			WHERE t.avatar_path = $1
			  AND (j.visibility = 'published' OR $2)
		)`, name, isAdmin).Scan(&referenced); err != nil {
		a.logger.Error("check avatar reference", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if !referenced {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-cache, must-revalidate")
	c.File(path)
}

func requestRoute(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return "unmatched"
}

func (a *App) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		a.logger.Warn("health database check failed")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	if err := database.CheckUpToDate(ctx, a.pool, a.config.MigrationsDir); err != nil {
		a.logger.Warn("health schema check failed", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (a *App) render(c *gin.Context, status int, name string, data PageData) {
	if data.User == nil {
		data.User = CurrentUser(c)
	}
	if data.CSRFToken == "" {
		data.CSRFToken = csrfToken(c)
	}
	c.HTML(status, name, data)
}

func (a *App) home(c *gin.Context) {
	data := PageData{AuthOpen: c.Query("auth") != "", AuthMode: c.DefaultQuery("auth", "login"), Next: safeNext(c.Query("next")), ProfileError: c.Query("profile_error"), AutoOpenAuth: true}
	if err := a.populateHome(c, &data); err != nil {
		a.logger.Error("load active jam", "error", err)
		data.Error = "Не удалось загрузить данные. Попробуйте позже."
	}
	if !a.recheckHomeDisclosure(c, &data) {
		return
	}
	a.render(c, http.StatusOK, "home.html", data)
}

func (a *App) recheckHomeDisclosure(c *gin.Context, data *PageData) bool {
	if data.Jam == nil {
		return true
	}
	current, err := a.loadPublishedJam(c.Request.Context(), data.Jam.ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && current.Stage != data.Jam.Stage {
		c.Redirect(http.StatusSeeOther, c.Request.URL.RequestURI())
		return false
	}
	if err != nil {
		a.logger.Error("recheck public jam", "error", err)
		a.writeError(c, http.StatusInternalServerError, "")
		return false
	}
	return true
}

func (a *App) activeJam(ctx context.Context) (*JamView, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT id, public_id, title, description, rules, max_team_size, submission_starts_at, evaluation_starts_at,
		       voting_starts_at, finishes_at, status_override
		FROM jams WHERE visibility = 'published' ORDER BY finishes_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var jam JamView
		var schedule Schedule
		var override *string
		if err := rows.Scan(&jam.ID, &jam.PublicID, &jam.Title, &jam.Description, &jam.Rules, &jam.MaxTeamSize, &schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override); err != nil {
			return nil, err
		}
		applyPublicJamSchedule(&jam, schedule, override, now)
		if jam.Stage != StageFinished {
			return &jam, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (a *App) authPage(mode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c) != nil {
			c.Redirect(http.StatusSeeOther, safeNext(c.Query("next")))
			return
		}
		data := PageData{AuthOpen: true, AuthMode: mode, Next: safeNext(c.Query("next"))}
		if err := a.populateHome(c, &data); err != nil {
			a.logger.Error("load home for authentication", "error", err)
			data.Error = "Не удалось загрузить данные. Попробуйте позже."
		}
		if !a.recheckHomeDisclosure(c, &data) {
			return
		}
		a.render(c, http.StatusOK, "home.html", data)
	}
}

func stageIndex(stage Stage) int {
	switch stage {
	case StageUpcoming:
		return 0
	case StageSubmission:
		return 1
	case StageEvaluation:
		return 2
	case StageVoting:
		return 3
	default:
		return 4
	}
}

package web

import (
	"bytes"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/gin-gonic/gin"
)

func TestSafeNext(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "/"},
		{"/teams/42", "/teams/42"},
		{"/jams/1/questionnaire?mode=edit", "/jams/1/questionnaire?mode=edit"},
		{"https://evil.example/", "/"},
		{"//evil.example/", "/"},
		{`/\evil`, "/"},
		{"relative", "/"},
	}
	for _, tt := range tests {
		if got := safeNext(tt.input); got != tt.want {
			t.Errorf("safeNext(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCSRFTokenSignature(t *testing.T) {
	app := &App{config: config.Config{CSRFSecret: []byte("01234567890123456789012345678901")}}
	token, err := app.newCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if !app.validCSRFSignature(token) {
		t.Fatal("new token did not validate")
	}
	if app.validCSRFSignature(token + "x") {
		t.Fatal("tampered token validated")
	}
	bound, err := app.newCSRFToken("session-a")
	if err != nil {
		t.Fatal(err)
	}
	if !app.validCSRFSignature(bound, "session-a") || app.validCSRFSignature(bound, "session-b") || app.validCSRFSignature(bound) {
		t.Fatal("session-bound CSRF token accepted with the wrong binding")
	}
}

func TestAvatarLookupRequiresPublishedJamForNonAdmin(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"JOIN jams j ON j.id = t.jam_id",
		"j.visibility = 'published' OR $2",
		"name, isAdmin",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("avatar lookup is missing %q", required)
		}
	}
}

func TestCreateAdminSerializesBootstrapAndRevokesSessions(t *testing.T) {
	source, err := os.ReadFile("admin_cli.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"pg_advisory_xact_lock($1)",
		"adminRoleLock",
		"DELETE FROM sessions WHERE user_id = $1",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("admin bootstrap is missing %q", required)
		}
	}
}

func TestAuthIdentityLimitAppliesAcrossIPs(t *testing.T) {
	app := &App{authLimit: newAuthAttemptLimiter()}
	for index, remote := range []string{"192.0.2.1:1000", "198.51.100.2:2000"} {
		request := httptest.NewRequest("POST", "/login", nil)
		request.RemoteAddr = remote
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		allowed := app.allowAuthAttempt(context, "login", "SameUser", 1, time.Minute)
		if allowed != (index == 0) {
			t.Fatalf("attempt %d from %s allowed=%v", index+1, remote, allowed)
		}
	}
}

func TestAuthRateLimitUsesClientIPOnlyFromTrustedProxies(t *testing.T) {
	for _, test := range []struct {
		name        string
		trusted     []string
		blockSecond bool
	}{
		{"untrusted proxy ignored", nil, true},
		{"trusted proxy honored", []string{"10.0.0.0/8"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &App{authLimit: newAuthAttemptLimiter()}
			router := gin.New()
			if err := router.SetTrustedProxies(test.trusted); err != nil {
				t.Fatal(err)
			}
			router.POST("/probe", func(c *gin.Context) {
				c.String(200, boolString(app.allowAuthAttempt(c, "register", "", 1, time.Minute)))
			})

			attempt := func(remote, xff string) bool {
				request := httptest.NewRequest("POST", "/probe", nil)
				request.RemoteAddr = remote
				request.Header.Set("X-Forwarded-For", xff)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				return recorder.Body.String() == "true"
			}
			if !attempt("10.0.0.1:1234", "203.0.113.9") {
				t.Fatal("first attempt was rejected")
			}
			if got := attempt("10.0.0.1:1234", "203.0.113.10"); got == test.blockSecond {
				t.Fatalf("second attempt allowed=%v, want blockSecond=%v", got, test.blockSecond)
			}
		})
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestTemplatesParseAndRenderHome(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	email := "user@example.test"
	deadline := time.Now().Add(time.Hour)
	data := PageData{
		User:      &User{ID: 1, Username: "archivist", Email: &email, Role: "user"},
		CSRFToken: "csrf-token",
		Jam: &JamView{
			ID: 1, Title: "Сигнал", Description: "Творческий джем", Rules: "Не опаздывать.",
			Stage: StageUpcoming, StageIndex: 0, NextStageAt: &deadline,
			NextStageRFC3339: deadline.Format(time.RFC3339), MaxTeamSize: 5,
		},
		Teams:   []HomeTeamView{{ID: 1, Name: "Ночной отдел", MemberCount: 2, MaxSize: 5, IsOwn: true}},
		Profile: &ProfileView{Username: "archivist", Email: email, TeamID: 1, TeamName: "Ночной отдел", TeamRole: "капитан"},
	}
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, "home.html", data); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("home template rendered no output")
	}
}

func TestPageTemplatesExecute(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	user := &User{ID: 1, Username: "admin", Role: "admin"}
	tests := []struct {
		name string
		data any
	}{
		{"admin_jams.html", jamAdminPageData{PageData: PageData{User: user, CSRFToken: "token"}}},
		{"admin_dashboard.html", adminDashboardData{User: user, CSRFToken: "token"}},
		{"admin_jam_form.html", jamAdminPageData{PageData: PageData{User: user, CSRFToken: "token"}}},
		{"admin_questionnaire.html", jamAdminPageData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_questionnaire_reports.html", adminQuestionnaireReportsData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_questionnaire_response.html", adminQuestionnaireResponseData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_themes.html", adminThemesPageData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_products.html", adminProductsPageData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}, Products: []ProductView{{ID: 1, TeamID: 1, TeamName: "Team", Status: "draft"}}}},
		{"admin_nominations.html", adminNominationsPageData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_votes.html", adminInterventionData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_bumps.html", adminInterventionData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
		{"admin_users.html", adminControlPageData{User: user, CSRFToken: "token"}},
		{"admin_user_detail.html", adminControlPageData{User: user, CSRFToken: "token"}},
		{"admin_teams.html", adminControlPageData{User: user, CSRFToken: "token"}},
		{"team_new.html", teamPageView{User: user, CSRFToken: "token", JamID: 1}},
		{"team_detail.html", teamPageView{User: user, CSRFToken: "token", Team: teamDetailView{ID: 1, Name: "Team"}}},
		{"team_invite.html", teamInviteView{User: user, CSRFToken: "token", TeamID: 1, TeamName: "Team"}},
		{"team_invite_issued.html", teamInviteView{User: user, CSRFToken: "token", Token: "invite-token", TeamID: 1}},
		{"questionnaire.html", questionnairePageData{User: user, CSRFToken: "token", JamID: 1, JamTitle: "Jam"}},
		{"product_edit.html", productPageData{User: user, CSRFToken: "token", JamID: 1, TeamID: 1, TeamName: "Team", Product: ProductView{Status: "draft"}}},
		{"products_list.html", productsListPageData{User: user, CSRFToken: "token", JamID: 1, JamTitle: "Jam"}},
		{"product_detail.html", productPageData{User: user, CSRFToken: "token", Product: ProductView{ID: 1, JamID: 1, TeamID: 1, Title: "Product"}}},
		{"nominations_list.html", nominationsPageData{User: user, CSRFToken: "token", JamID: 1, JamTitle: "Jam", Stage: StageVoting}},
		{"archive.html", archivePageData{User: user, CSRFToken: "token", Jams: []JamView{{ID: 1, Title: "Jam", Stage: StageFinished, Dates: []JamDateView{{}, {}, {}, {Moscow: "01.01.2026 15:00 МСК"}}}}}},
		{"error.html", errorPageData{Status: 404, Message: "Недоступно", RequestID: "request-id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tmpl.ExecuteTemplate(&output, tt.name, tt.data); err != nil {
				t.Fatal(err)
			}
			if output.Len() == 0 {
				t.Fatal("template rendered no output")
			}
		})
	}
}

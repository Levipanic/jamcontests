package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
)

func TestAdminVoteAndBumpInterventionsAffectAuthoritativeCounts(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createVotingHTTPFixture(t, ctx, pool)
	adminID := insertQuestionnaireReportUser(t, ctx, pool, "interventionadmin", "admin")
	if _, err := pool.Exec(ctx, `INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id) VALUES ($1,$2,$3,$4)`, fixture.voterID, fixture.nominationID, fixture.productBID, fixture.jamID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO product_bumps (user_id, product_id, jam_id, bump_count) VALUES ($1,$2,$3,5)`, fixture.voterID, fixture.productBID, fixture.jamID); err != nil {
		t.Fatal(err)
	}
	var voteID, bumpID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM nomination_votes WHERE user_id=$1 AND nomination_id=$2`, fixture.voterID, fixture.nominationID).Scan(&voteID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM product_bumps WHERE user_id=$1 AND product_id=$2`, fixture.voterID, fixture.productBID).Scan(&bumpID); err != nil {
		t.Fatal(err)
	}
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	adminSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", adminID, 21)
	dashboard := httptest.NewRecorder()
	dashboardRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	dashboardRequest.AddCookie(adminSession)
	router.ServeHTTP(dashboard, dashboardRequest)
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), "Видимость: published") || !strings.Contains(dashboard.Body.String(), "override (voting)") {
		t.Fatalf("admin dashboard omitted lifecycle state: status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/admin/jams/"+formatID(fixture.jamID)+"/votes", nil)
	get.AddCookie(adminSession)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin votes status=%d", recorder.Code)
	}
	csrfCookie := responseCookie(t, recorder.Result(), csrfCookieName)
	var productBCaptainID, selfVoteID int64
	if err := pool.QueryRow(ctx, `SELECT team.captain_user_id FROM products product JOIN teams team ON team.id=product.team_id WHERE product.id=$1`, fixture.productBID).Scan(&productBCaptainID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO nomination_votes (user_id,nomination_id,product_id,jam_id,invalidated_at,invalidated_by,invalidation_reason) VALUES ($1,$2,$3,$4,now(),$5,'test invalidation') RETURNING id`, productBCaptainID, fixture.nominationID, fixture.productBID, fixture.jamID, adminID).Scan(&selfVoteID); err != nil {
		t.Fatal(err)
	}
	selfRestore := performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/votes/"+formatID(selfVoteID)+"/restore", "restore")
	if !strings.Contains(selfRestore.Header().Get("Location"), "error=") {
		t.Fatal("restoring a self-vote did not report a conflict")
	}
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/votes/"+formatID(voteID)+"/invalidate", "invalidate")
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/bumps/"+formatID(bumpID)+"/invalidate", "invalidate")

	counts := httptest.NewRecorder()
	router.ServeHTTP(counts, httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil))
	if counts.Code != http.StatusOK || strings.Contains(counts.Body.String(), `"count":1`) {
		t.Fatalf("invalidated vote remained counted: %s", counts.Body.String())
	}
	bumpCounts := httptest.NewRecorder()
	router.ServeHTTP(bumpCounts, httptest.NewRequest(http.MethodGet, "/api/products/"+formatID(fixture.productBID)+"/bumps", nil))
	if bumpCounts.Code != http.StatusOK || !strings.Contains(bumpCounts.Body.String(), `"count":0`) {
		t.Fatalf("invalidated bumps remained counted: %s", bumpCounts.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE product_bumps SET bump_count=bump_count+1 WHERE id=$1`, bumpID); err != nil {
		t.Fatal(err)
	}
	newBumpCounts := httptest.NewRecorder()
	router.ServeHTTP(newBumpCounts, httptest.NewRequest(http.MethodGet, "/api/products/"+formatID(fixture.productBID)+"/bumps", nil))
	if newBumpCounts.Code != http.StatusOK || !strings.Contains(newBumpCounts.Body.String(), `"count":1`) {
		t.Fatalf("new bump after invalidation not counted: %s", newBumpCounts.Body.String())
	}
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/bumps/"+formatID(bumpID)+"/invalidate", "invalidate")

	voterSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", fixture.voterID, 22)
	voterCSRFRequest := httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil)
	voterCSRFRequest.AddCookie(voterSession)
	voterCSRFResponse := httptest.NewRecorder()
	router.ServeHTTP(voterCSRFResponse, voterCSRFRequest)
	voterCSRF := responseCookie(t, voterCSRFResponse.Result(), csrfCookieName)
	newVote := performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productCID, voterCSRF, voterSession)
	if newVote.Code != http.StatusOK {
		t.Fatalf("new vote after invalidation status=%d body=%s", newVote.Code, newVote.Body.String())
	}
	restoreConflict := performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/votes/"+formatID(voteID)+"/restore", "restore")
	if !strings.Contains(restoreConflict.Header().Get("Location"), "error=") {
		t.Fatal("restoring vote did not report active-selection conflict")
	}
	var newVoteID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM nomination_votes WHERE user_id=$1 AND nomination_id=$2 AND invalidated_at IS NULL`, fixture.voterID, fixture.nominationID).Scan(&newVoteID); err != nil {
		t.Fatal(err)
	}
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/votes/"+formatID(newVoteID)+"/invalidate", "invalidate")
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/votes/"+formatID(voteID)+"/restore", "restore")
	performAdminIntervention(t, router, adminSession, csrfCookie, "/admin/jams/"+formatID(fixture.jamID)+"/bumps/"+formatID(bumpID)+"/restore", "restore")
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_log WHERE action IN ('vote.invalidate','vote.restore','bump.invalidate','bump.restore')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 6 {
		t.Fatalf("intervention audits=%d, want 6", audits)
	}
	var auditHasMaterialState bool
	if err := pool.QueryRow(ctx, `SELECT before_data ?& array['invalidated_at','invalidated_by','invalidation_reason'] AND after_data ?& array['invalidated_at','invalidated_by','invalidation_reason'] FROM admin_audit_log WHERE action='bump.invalidate' ORDER BY id DESC LIMIT 1`).Scan(&auditHasMaterialState); err != nil {
		t.Fatal(err)
	}
	if !auditHasMaterialState {
		t.Fatal("bump intervention audit omitted material invalidation state")
	}
}

func performAdminIntervention(t *testing.T, router http.Handler, session, csrf *http.Cookie, path, action string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"reason": {"Исправление недействительной записи"}, "confirm": {action}, "csrf_token": {csrf.Value}}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

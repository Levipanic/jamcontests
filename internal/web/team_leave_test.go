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
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamLeaveDeletesLastMemberTeam verifies the cascade: when the last
// member (a solo captain) leaves, the whole team record is dissolved in the
// required dependency order and cannot leave orphaned rows behind.
func TestTeamLeaveDeletesLastMemberTeam(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	captainID := insertQuestionnaireReportUser(t, ctx, pool, "solo_captain", "user")
	voterID := insertQuestionnaireReportUser(t, ctx, pool, "solo_voter", "user")
	jamID, jamPublic, teamID, teamPublic := insertLeaveFixture(t, ctx, pool, captainID)

	var themeID int64
	if err := pool.QueryRow(ctx, `INSERT INTO jam_themes (jam_id, phrase) VALUES ($1, 'Theme') RETURNING id`, jamID).Scan(&themeID); err != nil {
		t.Fatal(err)
	}
	var productID, nominationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO products (team_id, jam_id, title, result_url, status, finalized_at, public_id)
		VALUES ($1, $2, 'Last product', 'https://example.example/result', 'final', now(), 'dddddddddddddddddd')
		RETURNING id`, teamID, jamID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO nominations (jam_id, kind, title, author_team_id, product_id)
		VALUES ($1, 'team', 'Solo nomination', $2, $3) RETURNING id`, jamID, teamID, productID).Scan(&nominationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_theme_selections (team_id, jam_id, theme_id, selected_by_user_id)
		VALUES ($1, $2, $3, $4)`, teamID, jamID, themeID, captainID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_eligibility_overrides (team_id, allowed, reason, admin_user_id)
		VALUES ($1, true, '', $2)`, teamID, captainID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO team_invites (team_id, token_hash, created_by)
		VALUES ($1, decode(repeat('ab', 32), 'hex'), $2)`, teamID, captainID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO product_bumps (user_id, product_id, jam_id, bump_count)
		VALUES ($1, $2, $3, 1)`, voterID, productID, jamID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)`, voterID, nominationID, productID, jamID); err != nil {
		t.Fatal(err)
	}

	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	adminSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", captainID, 90)
	captainLeave := performTeamLeaveRequest(t, router, adminSession, teamPublic)
	if !strings.Contains(captainLeave.Header().Get("Location"), "/") {
		t.Fatalf("solo captain leave location=%q", captainLeave.Header().Get("Location"))
	}

	var voteRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM nomination_votes WHERE jam_id=$1`, jamID).Scan(&voteRows); err != nil {
		t.Fatal(err)
	}
	if voteRows != 0 {
		t.Fatalf("team cascade left %d rows in nomination_votes", voteRows)
	}
	var nominationRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM nominations WHERE author_team_id=$1`, teamID).Scan(&nominationRows); err != nil {
		t.Fatal(err)
	}
	if nominationRows != 0 {
		t.Fatalf("team cascade left %d rows in nominations", nominationRows)
	}
	var bumpRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_bumps WHERE jam_id=$1`, jamID).Scan(&bumpRows); err != nil {
		t.Fatal(err)
	}
	if bumpRows != 0 {
		t.Fatalf("team cascade left %d rows in product_bumps", bumpRows)
	}
	var productRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE jam_id=$1`, jamID).Scan(&productRows); err != nil {
		t.Fatal(err)
	}
	if productRows != 0 {
		t.Fatalf("team cascade left %d rows in products", productRows)
	}
	tables := []string{
		"team_theme_selections",
		"team_eligibility_overrides",
		"team_invites",
		"team_members",
		"teams",
	}
	for _, table := range tables {
		var count int
		where := "id"
		if table != "teams" {
			where = "team_id"
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE `+where+`=$1`, teamID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("team cascade left %d rows in %s", count, table)
		}
	}
	response := disclosureRequest(router, "/jams/"+jamPublic+"/products")
	if strings.Contains(response.Body.String(), "Last product") {
		t.Fatal("deleted team product remained visible")
	}
}

// TestTeamLeaveRequiresNonCaptain verifies that a captain with other members
// still must transfer captaincy, and that a regular member leaves alone
// without dissolving the team.
func TestTeamLeaveRequiresNonCaptain(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	captainID := insertQuestionnaireReportUser(t, ctx, pool, "member_captain", "user")
	memberID := insertQuestionnaireReportUser(t, ctx, pool, "regular_member", "user")
	jamID, _, teamID, teamPublic := insertLeaveFixture(t, ctx, pool, captainID)
	if _, err := pool.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, memberID); err != nil {
		t.Fatal(err)
	}
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	captainSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", captainID, 91)
	captainLeave := performTeamLeaveRequest(t, router, captainSession, teamPublic)
	if !strings.Contains(captainLeave.Header().Get("Location"), "error=") {
		t.Fatal("captain leave with members did not report captaincy transfer requirement")
	}
	memberSession := insertQuestionnaireReportSession(t, ctx, pool, "test_session", memberID, 92)
	memberLeave := performTeamLeaveRequest(t, router, memberSession, teamPublic)
	if !strings.Contains(memberLeave.Header().Get("Location"), "/") {
		t.Fatalf("member leave location=%q", memberLeave.Header().Get("Location"))
	}
	var teamAlive bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id=$1)`, teamID).Scan(&teamAlive); err != nil {
		t.Fatal(err)
	}
	if !teamAlive {
		t.Fatal("regular member leave dissolved the team")
	}
	var memberGone bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM team_members WHERE team_id=$1 AND user_id=$2)`, teamID, memberID).Scan(&memberGone); err != nil {
		t.Fatal(err)
	}
	if !memberGone {
		t.Fatal("regular member stayed in team after leaving")
	}
}

func insertLeaveFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, captainID int64) (int64, string, int64, string) {
	t.Helper()
	var jamID int64
	var jamPublic string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jams (title, description, rules, submission_starts_at, evaluation_starts_at,
		                  voting_starts_at, finishes_at, max_team_size, visibility, status_override, public_id)
		VALUES ('Leave jam', '', '', clock_timestamp()+interval '10 days',
		        clock_timestamp()+interval '12 days', clock_timestamp()+interval '14 days',
		        clock_timestamp()+interval '16 days', 4, 'published', 'upcoming', 'eeeeeeeeeeeeeeeeee')
		RETURNING id, public_id`).Scan(&jamID, &jamPublic); err != nil {
		t.Fatal(err)
	}
	var teamID int64
	var teamPublic string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `
		INSERT INTO teams (jam_id, name, captain_user_id, public_id)
		VALUES ($1, 'Leave team', $2, 'ffffffffffffffffff') RETURNING id, public_id`, jamID, captainID).Scan(&teamID, &teamPublic); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, captainID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return jamID, jamPublic, teamID, teamPublic
}

func performTeamLeaveRequest(t *testing.T, router http.Handler, session *http.Cookie, teamPublic string) *httptest.ResponseRecorder {
	t.Helper()
	csrfPage := httptest.NewRecorder()
	csrfRequest := httptest.NewRequest(http.MethodGet, "/teams/"+teamPublic, nil)
	csrfRequest.AddCookie(session)
	router.ServeHTTP(csrfPage, csrfRequest)
	if csrfPage.Code != http.StatusOK {
		t.Fatalf("team page status=%d body=%s", csrfPage.Code, csrfPage.Body.String())
	}
	csrfCookie := responseCookie(t, csrfPage.Result(), csrfCookieName)
	form := url.Values{"csrf_token": {csrfCookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "/teams/"+teamPublic+"/leave", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

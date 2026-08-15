package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVotingHTTPMutationCountsSelfVoteAndConcurrency(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createVotingHTTPFixture(t, ctx, pool)

	cfg := config.Config{
		Environment: "test", CSRFSecret: []byte("01234567890123456789012345678901"),
		SessionCookie: "test_session", SessionTTL: time.Hour,
		TemplatesDir: "../../templates", StaticDir: "../../static", AvatarDir: t.TempDir(), MaxAvatarBytes: 2 << 20,
	}
	router := New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionCookie := insertVotingTestSession(t, ctx, pool, cfg.SessionCookie, fixture.voterID)

	countsRequest := httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil)
	countsRecorder := httptest.NewRecorder()
	router.ServeHTTP(countsRecorder, countsRequest)
	if countsRecorder.Code != http.StatusOK {
		t.Fatalf("initial counts status = %d, body=%s", countsRecorder.Code, countsRecorder.Body.String())
	}
	csrfCookie := responseCookie(t, countsRecorder.Result(), csrfCookieName)

	guestRecorder := performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productBID, csrfCookie, nil)
	if guestRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("guest vote status = %d, want 401", guestRecorder.Code)
	}
	authCSRFRequest := httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil)
	authCSRFRequest.AddCookie(sessionCookie)
	authCSRFRecorder := httptest.NewRecorder()
	router.ServeHTTP(authCSRFRecorder, authCSRFRequest)
	csrfCookie = responseCookie(t, authCSRFRecorder.Result(), csrfCookieName)

	response := performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productBID, csrfCookie, sessionCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("vote status = %d, body=%s", response.Code, response.Body.String())
	}
	var vote voteResponse
	if err := json.Unmarshal(response.Body.Bytes(), &vote); err != nil {
		t.Fatal(err)
	}
	if vote.NominationID != fixture.nominationID || vote.SelectedProductID != fixture.productBID {
		t.Fatalf("unexpected vote response: %+v", vote)
	}

	response = performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productCID, csrfCookie, sessionCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("change vote status = %d, body=%s", response.Code, response.Body.String())
	}
	response = performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productAID, csrfCookie, sessionCookie)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self vote status = %d, body=%s", response.Code, response.Body.String())
	}

	countsRequest = httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil)
	countsRecorder = httptest.NewRecorder()
	router.ServeHTTP(countsRecorder, countsRequest)
	if countsRecorder.Code != http.StatusOK {
		t.Fatalf("updated counts status = %d", countsRecorder.Code)
	}
	var counts voteCountsResponse
	if err := json.Unmarshal(countsRecorder.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	assertVoteCount(t, counts.Counts, fixture.nominationID, fixture.productAID, 0)
	assertVoteCount(t, counts.Counts, fixture.nominationID, fixture.productBID, 0)
	assertVoteCount(t, counts.Counts, fixture.nominationID, fixture.productCID, 1)

	if _, err := pool.Exec(ctx, `DELETE FROM nomination_votes WHERE user_id=$1 AND nomination_id=$2`, fixture.voterID, fixture.nominationID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for _, productID := range []int64{fixture.productBID, fixture.productCID} {
		wait.Add(1)
		go func(productID int64) {
			defer wait.Done()
			<-start
			responses <- performVoteRequest(router, fixture.jamID, fixture.nominationID, productID, csrfCookie, sessionCookie)
		}(productID)
	}
	close(start)
	wait.Wait()
	close(responses)
	for recorder := range responses {
		if recorder.Code != http.StatusOK {
			t.Fatalf("concurrent vote status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
	}
	var activeSelections int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM nomination_votes WHERE user_id=$1 AND nomination_id=$2`, fixture.voterID, fixture.nominationID).Scan(&activeSelections); err != nil {
		t.Fatal(err)
	}
	if activeSelections != 1 {
		t.Fatalf("concurrent active selections = %d, want 1", activeSelections)
	}

	if _, err := pool.Exec(ctx, `UPDATE jams SET status_override='finished' WHERE id=$1`, fixture.jamID); err != nil {
		t.Fatal(err)
	}
	countsRequest = httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamID), nil)
	countsRecorder = httptest.NewRecorder()
	router.ServeHTTP(countsRecorder, countsRequest)
	if countsRecorder.Code != http.StatusNotFound {
		t.Fatalf("finished counts status = %d, want 404", countsRecorder.Code)
	}
	response = performVoteRequest(router, fixture.jamID, fixture.nominationID, fixture.productBID, csrfCookie, sessionCookie)
	if response.Code != http.StatusConflict {
		t.Fatalf("finished vote status = %d, want 409", response.Code)
	}
}

type votingHTTPFixture struct {
	jamID        int64
	voterID      int64
	nominationID int64
	productAID   int64
	productBID   int64
	productCID   int64
}

func createVotingHTTPFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) votingHTTPFixture {
	t.Helper()
	fixture := votingHTTPFixture{}
	fixture.voterID = insertVotingTestUser(t, ctx, pool, "voter")
	captainBID := insertVotingTestUser(t, ctx, pool, "captainb")
	captainCID := insertVotingTestUser(t, ctx, pool, "captainc")
	if err := pool.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override, max_team_size)
		VALUES ('Voting HTTP', 'published', now()-interval '3 hours', now()-interval '2 hours', now()-interval '1 hour', now()+interval '1 hour', 'voting', 5)
		RETURNING id`).Scan(&fixture.jamID); err != nil {
		t.Fatal(err)
	}
	fixture.productAID = insertVotingTestTeamProduct(t, ctx, pool, fixture.jamID, fixture.voterID, "Team A", "Product A")
	fixture.productBID = insertVotingTestTeamProduct(t, ctx, pool, fixture.jamID, captainBID, "Team B", "Product B")
	fixture.productCID = insertVotingTestTeamProduct(t, ctx, pool, fixture.jamID, captainCID, "Team C", "Product C")
	if err := pool.QueryRow(ctx, `INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Choice') RETURNING id`, fixture.jamID).Scan(&fixture.nominationID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func insertVotingTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertVotingTestTeamProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID, captainID int64, teamName, productTitle string) int64 {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var teamID int64
	if err = tx.QueryRow(ctx, `INSERT INTO teams (jam_id, name, captain_user_id) VALUES ($1, $2, $3) RETURNING id`, jamID, teamName, captainID).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, captainID); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO products (jam_id, team_id, title, result_url, status, finalized_at)
		VALUES ($1, $2, $3, 'https://example.test/result', 'final', now()) RETURNING id`, jamID, teamID, productTitle).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return productID
}

func insertVotingTestSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cookieName string, userID int64) *http.Cookie {
	t.Helper()
	raw := bytes.Repeat([]byte{7}, 32)
	hash := sha256.Sum256(raw)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now()+interval '1 hour')`, hash[:], userID); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: cookieName, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/"}
}

func performVoteRequest(router http.Handler, jamID, nominationID, productID int64, csrfCookie, sessionCookie *http.Cookie) *httptest.ResponseRecorder {
	body, _ := json.Marshal(voteRequest{ProductID: productID})
	request := httptest.NewRequest(http.MethodPost, votingVotePath(jamID, nominationID), bytes.NewReader(body))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrfCookie.Value)
	request.AddCookie(csrfCookie)
	if sessionCookie != nil {
		request.AddCookie(sessionCookie)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func responseCookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response cookie %q not found", name)
	return nil
}

func votingCountsPath(jamID int64) string {
	return "/api/jams/" + formatID(jamID) + "/vote-counts"
}

func votingVotePath(jamID, nominationID int64) string {
	return "/api/jams/" + formatID(jamID) + "/nominations/" + formatID(nominationID) + "/vote"
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func assertVoteCount(t *testing.T, counts []voteCount, nominationID, productID, want int64) {
	t.Helper()
	for _, count := range counts {
		if count.NominationID == nominationID && count.ProductID == productID {
			if count.Count != want {
				t.Fatalf("count for nomination=%d product=%d is %d, want %d", nominationID, productID, count.Count, want)
			}
			return
		}
	}
	t.Fatalf("count for nomination=%d product=%d is missing", nominationID, productID)
}

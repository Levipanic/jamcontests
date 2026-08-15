package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFinishedNominationResultsSingleTieAndZero(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createVotingHTTPFixture(t, ctx, pool)
	var tieNominationID, zeroNominationID int64
	if err := pool.QueryRow(ctx, `INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Tie result') RETURNING id`, fixture.jamID).Scan(&tieNominationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Zero result') RETURNING id`, fixture.jamID).Scan(&zeroNominationID); err != nil {
		t.Fatal(err)
	}
	voters := make([]int64, 5)
	for index := range voters {
		voters[index] = insertVotingTestUser(t, ctx, pool, "resultvoter"+formatID(int64(index+1)))
	}
	insertResultVote(t, ctx, pool, voters[0], fixture.nominationID, fixture.productBID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[1], fixture.nominationID, fixture.productBID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[2], fixture.nominationID, fixture.productBID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[3], fixture.nominationID, fixture.productCID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[0], tieNominationID, fixture.productAID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[1], tieNominationID, fixture.productAID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[2], tieNominationID, fixture.productBID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[3], tieNominationID, fixture.productBID, fixture.jamID)
	insertResultVote(t, ctx, pool, voters[4], tieNominationID, fixture.productCID, fixture.jamID)
	if _, err := pool.Exec(ctx, `UPDATE jams SET status_override='finished' WHERE id=$1`, fixture.jamID); err != nil {
		t.Fatal(err)
	}

	nominations := []NominationView{{ID: fixture.nominationID}, {ID: tieNominationID}, {ID: zeroNominationID}}
	app := &App{pool: pool}
	if err := app.populateFinishedResults(ctx, fixture.jamID, nominations); err != nil {
		t.Fatal(err)
	}
	if nominations[0].Result.TotalVotes != 4 || len(nominations[0].Result.Winners) != 1 || nominations[0].Result.Winners[0].ProductID != fixture.productBID || nominations[0].Result.Winners[0].VoteCount != 3 {
		t.Fatalf("unexpected single winner result: %+v", nominations[0].Result)
	}
	if nominations[1].Result.TotalVotes != 5 || len(nominations[1].Result.Winners) != 2 {
		t.Fatalf("unexpected tie result: %+v", nominations[1].Result)
	}
	if nominations[2].Result.TotalVotes != 0 || len(nominations[2].Result.Winners) != 0 {
		t.Fatalf("unexpected zero-vote result: %+v", nominations[2].Result)
	}

	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/jams/"+fixture.jamPublic+"/nominations", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("finished results status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	html := recorder.Body.String()
	for _, required := range []string{"Победитель номинации", "Tie result", "Zero result", "Победитель не определён"} {
		if !strings.Contains(html, required) {
			t.Errorf("finished results lack %q", required)
		}
	}
	for _, forbidden := range []string{"data-vote-action", "data-vote-product", "/static/js/voting.js"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("finished results contain voting control %q", forbidden)
		}
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, votingCountsPath(fixture.jamPublic), nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("finished live counts status=%d, want 404", recorder.Code)
	}
}

func insertResultVote(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, nominationID, productID, jamID int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id) VALUES ($1, $2, $3, $4)`, userID, nominationID, productID, jamID); err != nil {
		t.Fatal(err)
	}
}

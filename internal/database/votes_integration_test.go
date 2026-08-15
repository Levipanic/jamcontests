package database_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVoteConstraintsAndSelectionChange(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createVoteFixture(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)`, fixture.voterID, fixture.nominationID, fixture.productAID, fixture.jamID); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)`, fixture.voterID, fixture.nominationID, fixture.productBID, fixture.jamID)
	assertPostgreSQLCode(t, err, "23505")

	_, err = pool.Exec(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)`, fixture.otherVoterID, fixture.nominationID, fixture.crossJamProductID, fixture.jamID)
	assertPostgreSQLCode(t, err, "23503")
	_, err = pool.Exec(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)`, fixture.otherVoterID, fixture.crossJamNominationID, fixture.productAID, fixture.jamID)
	assertPostgreSQLCode(t, err, "23503")

	var selectedProductID int64
	if err = pool.QueryRow(ctx, `
		INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, nomination_id) WHERE invalidated_at IS NULL DO UPDATE SET product_id=EXCLUDED.product_id, updated_at=clock_timestamp()
		RETURNING product_id`, fixture.voterID, fixture.nominationID, fixture.productBID, fixture.jamID).Scan(&selectedProductID); err != nil {
		t.Fatal(err)
	}
	if selectedProductID != fixture.productBID {
		t.Fatalf("selected product = %d, want %d", selectedProductID, fixture.productBID)
	}
	var selections int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM nomination_votes WHERE user_id=$1 AND nomination_id=$2`, fixture.voterID, fixture.nominationID).Scan(&selections); err != nil {
		t.Fatal(err)
	}
	if selections != 1 {
		t.Fatalf("active selections = %d, want 1", selections)
	}
}

func TestConcurrentVoteUpsertsLeaveOneSelection(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createVoteFixture(t, ctx, pool)

	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, productID := range []int64{fixture.productAID, fixture.productBID} {
		wait.Add(1)
		go func(productID int64) {
			defer wait.Done()
			<-start
			_, err := pool.Exec(ctx, `
				INSERT INTO nomination_votes (user_id, nomination_id, product_id, jam_id)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id, nomination_id) WHERE invalidated_at IS NULL DO UPDATE SET
				    product_id=EXCLUDED.product_id, updated_at=clock_timestamp()`,
				fixture.voterID, fixture.nominationID, productID, fixture.jamID)
			errorsChannel <- err
		}(productID)
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	var selections int
	var selectedProductID int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(product_id) FROM nomination_votes
		WHERE user_id=$1 AND nomination_id=$2`, fixture.voterID, fixture.nominationID).Scan(&selections, &selectedProductID); err != nil {
		t.Fatal(err)
	}
	if selections != 1 || selectedProductID != fixture.productAID && selectedProductID != fixture.productBID {
		t.Fatalf("unexpected concurrent result: count=%d product=%d", selections, selectedProductID)
	}
}

type voteFixture struct {
	jamID                int64
	voterID              int64
	otherVoterID         int64
	nominationID         int64
	productAID           int64
	productBID           int64
	crossJamProductID    int64
	crossJamNominationID int64
}

func createVoteFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) voteFixture {
	t.Helper()
	fixture := voteFixture{}
	for index, target := range []*int64{&fixture.voterID, &fixture.otherVoterID} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`,
			"voter"+string(rune('a'+index))).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	fixture.jamID = insertVoteJam(t, ctx, pool, "Voting jam")
	captainAID := insertVoteUser(t, ctx, pool, "captaina")
	captainBID := insertVoteUser(t, ctx, pool, "captainb")
	fixture.productAID = insertVoteTeamProduct(t, ctx, pool, fixture.jamID, captainAID, "Team A", "Product A")
	fixture.productBID = insertVoteTeamProduct(t, ctx, pool, fixture.jamID, captainBID, "Team B", "Product B")
	if err := pool.QueryRow(ctx, `
		INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Choice') RETURNING id`, fixture.jamID).Scan(&fixture.nominationID); err != nil {
		t.Fatal(err)
	}
	otherJamID := insertVoteJam(t, ctx, pool, "Other jam")
	otherCaptainID := insertVoteUser(t, ctx, pool, "captainc")
	fixture.crossJamProductID = insertVoteTeamProduct(t, ctx, pool, otherJamID, otherCaptainID, "Team C", "Product C")
	if err := pool.QueryRow(ctx, `
		INSERT INTO nominations (jam_id, kind, title) VALUES ($1, 'curator', 'Other choice') RETURNING id`, otherJamID).Scan(&fixture.crossJamNominationID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func insertVoteJam(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, max_team_size)
		VALUES ($1, 'published', now()-interval '3 hours', now()-interval '2 hours', now()-interval '1 hour', now()+interval '1 hour', 5)
		RETURNING id`, title).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertVoteUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertVoteTeamProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jamID, captainID int64, teamName, productTitle string) int64 {
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

func assertPostgreSQLCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("PostgreSQL error = %v, want code %s", err, code)
	}
}

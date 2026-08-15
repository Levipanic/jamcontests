package database_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigrationsApplyToIsolatedPostgreSQLSchema(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "test_") {
		t.Fatalf("migrations are not isolated: current schema is %q", schema)
	}

	var migrations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations == 0 {
		t.Fatal("no migrations were applied")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (username, password_hash)
		VALUES ('Archivist', 'test-password-hash')`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO users (username, password_hash)
		VALUES ('archivist', 'test-password-hash')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("case-insensitive username constraint returned %v, want PostgreSQL 23505", err)
	}

	var votesTableExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('nomination_votes') IS NOT NULL`).Scan(&votesTableExists); err != nil {
		t.Fatal(err)
	}
	if !votesTableExists {
		t.Fatal("latest domain migration was not applied")
	}
}

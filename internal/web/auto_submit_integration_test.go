package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/testdb"
)

// TestDraftAutoSubmitsAfterSubmission verifies the lazy auto-submit rule:
// once the effective stage passed submission, a draft product is disclosed
// exactly like a finally submitted one, without any cron or status write.
func TestDraftAutoSubmitsAfterSubmission(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var jamID int64
	var jamPublic string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jams (title, description, rules, submission_starts_at, evaluation_starts_at,
		                  voting_starts_at, finishes_at, max_team_size, visibility, status_override, public_id)
		VALUES ('Auto submit jam', '', '', clock_timestamp()+interval '10 days',
		        clock_timestamp()+interval '12 days', clock_timestamp()+interval '14 days',
		        clock_timestamp()+interval '16 days', 4, 'published', 'upcoming', 'a1a1a1a1a1a1a1a1a1')
		RETURNING id, public_id`).Scan(&jamID, &jamPublic); err != nil {
		t.Fatal(err)
	}
	var themeID int64
	if err := pool.QueryRow(ctx, `INSERT INTO jam_themes (jam_id, phrase) VALUES ($1, 'Auto theme') RETURNING id`, jamID).Scan(&themeID); err != nil {
		t.Fatal(err)
	}
	var captainID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('autocaptain', 'test', 'user') RETURNING id`).Scan(&captainID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var teamID int64
	var teamPublic string
	if err = tx.QueryRow(ctx, `
		INSERT INTO teams (jam_id, name, description, captain_user_id, public_id)
		VALUES ($1, 'Auto team', '', $2, 'b2b2b2b2b2b2b2b2b2') RETURNING id, public_id`, jamID, captainID).Scan(&teamID, &teamPublic); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, captainID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_theme_selections (team_id, jam_id, theme_id, selected_by_user_id) VALUES ($1, $2, $3, $4)`, teamID, jamID, themeID, captainID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	const draftTitleSentinel = "DRAFT-PRODUCT-SENTINEL"
	var productID int64
	var productPublic string
	if err := pool.QueryRow(ctx, `
		INSERT INTO products (team_id, jam_id, title, result_url, status, public_id)
		VALUES ($1, $2, $3, 'https://example.example/result', 'draft', 'c3c3c3c3c3c3c3c3c3')
		RETURNING id, public_id`, teamID, jamID, draftTitleSentinel).Scan(&productID, &productPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO nominations (jam_id, kind, title, author_team_id, product_id)
		VALUES ($1, 'team', 'Auto nomination', $2, $3)`, jamID, teamID, productID); err != nil {
		t.Fatal(err)
	}

	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	setStage := func(stage string) {
		if _, err := pool.Exec(ctx, `UPDATE jams SET status_override=$2 WHERE id=$1`, jamID, stage); err != nil {
			t.Fatal(err)
		}
	}

	const productListPath = "/jams/"
	assertList := func(stage string, disclosed bool) {
		response := disclosureRequest(router, productListPath+jamPublic+"/products")
		if (response.Code == http.StatusOK) != disclosed || strings.Contains(response.Body.String(), draftTitleSentinel) != disclosed {
			t.Fatalf("%s: products list disclosed=%v status=%d body=%s", stage, disclosed, response.Code, response.Body.String())
		}
	}
	assertDetail := func(stage string, disclosed bool) {
		response := disclosureRequest(router, "/products/"+productPublic)
		if (response.Code == http.StatusOK) != disclosed || strings.Contains(response.Body.String(), draftTitleSentinel) != disclosed {
			t.Fatalf("%s: product detail disclosed=%v status=%d body=%s", stage, disclosed, response.Code, response.Body.String())
		}
	}
	assertHome := func(stage string, disclosed bool) {
		response := disclosureRequest(router, "/jams/"+jamPublic)
		if (response.Code == http.StatusOK) != true || strings.Contains(response.Body.String(), draftTitleSentinel) != disclosed {
			t.Fatalf("%s: home product block disclosed=%v body=%s", stage, disclosed, response.Body.String())
		}
	}

	setStage("submission")
	assertList("submission", false)
	assertDetail("submission", false)
	assertHome("submission", false)

	setStage("evaluation")
	assertList("evaluation", true)
	assertDetail("evaluation", true)
	assertHome("evaluation", true)
	bumpResponse := disclosureRequest(router, "/api/products/"+productPublic+"/bumps")
	if bumpResponse.Code != http.StatusOK || !strings.Contains(bumpResponse.Body.String(), `"count"`) {
		t.Fatalf("evaluation: bump counter unavailable for auto-submitted draft: status=%d body=%s", bumpResponse.Code, bumpResponse.Body.String())
	}

	setStage("voting")
	assertList("voting", true)
	assertDetail("voting", true)
	assertHome("voting", true)
	nominations := disclosureRequest(router, "/jams/"+jamPublic+"/nominations")
	if nominations.Code != http.StatusOK || !strings.Contains(nominations.Body.String(), "Auto nomination") || !strings.Contains(nominations.Body.String(), draftTitleSentinel) {
		t.Fatalf("voting: team nomination of auto-submitted draft not disclosed: status=%d body=%s", nominations.Code, nominations.Body.String())
	}

	setStage("finished")
	assertList("finished", true)
	assertDetail("finished", true)
	assertHome("finished", true)
}

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
)

func TestPublicDisclosureMatrixAcrossVisibilityAndStages(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createVotingHTTPFixture(t, ctx, pool)
	const themeSentinel = "THEME-SENTINEL-PRIVATE"
	const nominationSentinel = "NOMINATION-SENTINEL-PRIVATE"
	var themeID, productTeamID int64
	var productTeamPublic string
	if err := pool.QueryRow(ctx, `INSERT INTO jam_themes (jam_id,phrase) VALUES ($1,$2) RETURNING id`, fixture.jamID, themeSentinel).Scan(&themeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT team_id FROM products WHERE id=$1`, fixture.productBID).Scan(&productTeamID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT public_id FROM teams WHERE id=$1`, productTeamID).Scan(&productTeamPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_theme_selections (team_id,jam_id,theme_id,selected_by_user_id) SELECT team.id,team.jam_id,$2,team.captain_user_id FROM teams team WHERE team.jam_id=$1`, fixture.jamID, themeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nominations (jam_id,kind,title,author_team_id,product_id) VALUES ($1,'team',$2,$3,$4)`, fixture.jamID, nominationSentinel, productTeamID, fixture.productBID); err != nil {
		t.Fatal(err)
	}
	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))

	type expectation struct {
		stage                         Stage
		products, nominations         bool
		votes, bumps                  bool
		themes, outsiderTeamSelection bool
		author                        bool
	}
	stages := []expectation{
		{stage: StageUpcoming},
		{stage: StageSubmission, themes: true},
		{stage: StageEvaluation, products: true, bumps: true, themes: true, outsiderTeamSelection: true},
		{stage: StageVoting, products: true, nominations: true, votes: true, bumps: true, themes: true, outsiderTeamSelection: true},
		{stage: StageFinished, products: true, nominations: true, bumps: true, themes: true, outsiderTeamSelection: true, author: true},
	}
	for _, test := range stages {
		t.Run(string(test.stage), func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published',status_override=$2 WHERE id=$1`, fixture.jamID, test.stage); err != nil {
				t.Fatal(err)
			}
			assertDisclosureResponse(t, router, "/jams/"+fixture.jamPublic+"/products", test.products, "Product B")
			assertDisclosureResponse(t, router, "/products/"+fixture.productBPublic, test.products, "Product B")
			nominations := disclosureRequest(router, "/jams/"+fixture.jamPublic+"/nominations")
			if (nominations.Code == http.StatusOK) != test.nominations || strings.Contains(nominations.Body.String(), nominationSentinel) != test.nominations {
				t.Fatalf("nominations status=%d body=%s", nominations.Code, nominations.Body.String())
			}
			if strings.Contains(nominations.Body.String(), "Автор номинации:") != test.author {
				t.Fatalf("team nomination authorship disclosure mismatch: %s", nominations.Body.String())
			}
			assertDisclosureResponse(t, router, votingCountsPath(fixture.jamPublic), test.votes, `"counts"`)
			assertDisclosureResponse(t, router, "/api/products/"+fixture.productBPublic+"/bumps", test.bumps, `"count"`)
			home := disclosureRequest(router, "/jams/"+fixture.jamPublic)
			if strings.Contains(home.Body.String(), themeSentinel) != test.themes {
				t.Fatalf("theme disclosure mismatch: %s", home.Body.String())
			}
			team := disclosureRequest(router, "/teams/"+productTeamPublic)
			selectionVisible := strings.Contains(team.Body.String(), "<dt>Тема</dt>") && strings.Contains(team.Body.String(), themeSentinel)
			if selectionVisible != test.outsiderTeamSelection {
				t.Fatalf("team selection disclosure mismatch: %s", team.Body.String())
			}
		})
	}

	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='draft',status_override='finished' WHERE id=$1`, fixture.jamID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/jams/" + fixture.jamPublic,
		"/jams/" + fixture.jamPublic + "/products",
		"/products/" + fixture.productBPublic,
		"/jams/" + fixture.jamPublic + "/nominations",
		votingCountsPath(fixture.jamPublic),
		"/api/products/" + fixture.productBPublic + "/bumps",
	} {
		response := disclosureRequest(router, path)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), themeSentinel) || strings.Contains(response.Body.String(), nominationSentinel) || strings.Contains(response.Body.String(), "Product B") {
			t.Fatalf("draft leaked through %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func assertDisclosureResponse(t *testing.T, router http.Handler, path string, disclosed bool, sentinel string) {
	t.Helper()
	response := disclosureRequest(router, path)
	if (response.Code == http.StatusOK) != disclosed || strings.Contains(response.Body.String(), sentinel) != disclosed {
		t.Fatalf("%s disclosure=%v status=%d body=%s", path, disclosed, response.Code, response.Body.String())
	}
}

func disclosureRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBumpStageBoundaries(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, StageFinished, Stage("unknown")} {
		if canMutateBumps(stage) {
			t.Fatalf("bumps mutable at %q", stage)
		}
	}
	for _, stage := range []Stage{StageEvaluation, StageVoting} {
		if !canMutateBumps(stage) {
			t.Fatalf("bumps immutable at %q", stage)
		}
	}
	for _, stage := range []Stage{StageUpcoming, StageSubmission, Stage("unknown")} {
		if canDiscloseBumps(stage) {
			t.Fatalf("bumps disclosed at %q", stage)
		}
	}
	for _, stage := range []Stage{StageEvaluation, StageVoting, StageFinished} {
		if !canDiscloseBumps(stage) {
			t.Fatalf("bumps hidden at %q", stage)
		}
	}
}

func TestBumpsMigrationHasAggregateIntegrity(t *testing.T) {
	body, err := os.ReadFile("../../migrations/005_bumps.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"PRIMARY KEY (user_id, product_id)",
		"REFERENCES users(id) ON DELETE RESTRICT",
		"FOREIGN KEY (product_id, jam_id)",
		"REFERENCES products(id, jam_id) ON DELETE RESTRICT",
		"CHECK (bump_count > 0)",
		"last_bumped_at timestamptz NOT NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration lacks %q", required)
		}
	}
}

func TestBumpSQLIsAtomicAndUsesDatabaseClock(t *testing.T) {
	body, err := os.ReadFile("bumps.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"FOR SHARE OF jam",
		"ON CONFLICT (user_id, product_id) DO UPDATE",
		"product_bumps.bump_count + 1",
		"last_bumped_at <= clock_timestamp() - interval '1 minute'",
		"jam.status_override IN ('evaluation', 'voting')",
		"clock_timestamp() < jam.finishes_at",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("bump SQL lacks %q", required)
		}
	}
}

func TestPublicProductQueriesAggregateBumpsWithoutBumpOrdering(t *testing.T) {
	body, err := os.ReadFile("products.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"SELECT SUM(bump.bump_count-bump.invalidated_count)::bigint",
		"jam.status_override IN ('evaluation', 'voting', 'finished')",
		"ORDER BY p.finalized_at, p.id",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("public product query lacks %q", required)
		}
	}
	if strings.Contains(source, "ORDER BY bump") {
		t.Fatal("public products must not be ordered by bump data")
	}
}

func TestBumpTemplatesShowCountsWithoutPrivateProductFields(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	user := &User{ID: 7, Username: "user"}
	product := ProductView{
		ID: 11, JamID: 2, TeamID: 3, TeamName: "Team", Title: "Result",
		ResultURL: "https://example.test/result", Theme: "Theme", BumpCount: 42,
		Notes: "PRIVATE NOTES", NominationTitle: "PRIVATE NOMINATION",
	}
	tests := []struct {
		name string
		data any
	}{
		{"products_list.html", productsListPageData{User: user, CSRFToken: "token", JamID: 2, JamTitle: "Jam", Products: []ProductView{product}, Stage: StageEvaluation, BumpsMutable: true}},
		{"product_detail.html", productPageData{User: user, CSRFToken: "token", Product: product, Stage: StageEvaluation, BumpsMutable: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tmpl.ExecuteTemplate(&output, tt.name, tt.data); err != nil {
				t.Fatal(err)
			}
			html := output.String()
			for _, required := range []string{`data-bump-product="11"`, `data-bump-count`, `data-bump-action`, `>42</strong>`, `/static/js/bumps.js`, `meta name="csrf-token"`} {
				if !strings.Contains(html, required) {
					t.Errorf("rendered template lacks %q", required)
				}
			}
			if strings.Contains(html, product.Notes) || strings.Contains(html, product.NominationTitle) {
				t.Fatal("public bump UI disclosed private product fields")
			}
		})
	}
}

func TestFinishedBumpTemplateIsReadOnly(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	data := productPageData{User: &User{ID: 7}, Product: ProductView{ID: 11, BumpCount: 5}, Stage: StageFinished}
	if err := tmpl.ExecuteTemplate(&output, "product_detail.html", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "data-bump-action") || !strings.Contains(output.String(), ">5</strong>") {
		t.Fatal("finished bump count must be visible and read-only")
	}
}

func TestBumpJavaScriptUsesAPIAndCSRF(t *testing.T) {
	body, err := os.ReadFile("../../static/js/bumps.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{"/api/products/${panel.dataset.bumpProduct}/bumps", `"X-CSRF-Token": csrf`, "data-bump-count", "visibilitychange", "15000"} {
		if !strings.Contains(source, required) {
			t.Errorf("bump JavaScript lacks %q", required)
		}
	}
}

package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestValidateExternalURL(t *testing.T) {
	valid := []string{
		"https://example.com/work",
		"http://localhost:8080/result?q=1#part",
		"HTTPS://example.com/a%20b",
	}
	for _, value := range valid {
		if err := validateExternalURL(value); err != nil {
			t.Errorf("expected %q to be valid: %v", value, err)
		}
	}
	invalid := []string{
		"", "example.com/work", "//example.com/work", "ftp://example.com/work",
		"https://user:pass@example.com/work", "https:///work", "https://example.com\\@evil.test/",
		" https://example.com/work", "https://example.com/work\nnext", "https://example.com/%0Ahidden", "javascript:alert(1)",
	}
	for _, value := range invalid {
		if err := validateExternalURL(value); err == nil {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}

func TestPublicProductTemplateEscapesUserText(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	data := productPageData{Product: ProductView{ID: 1, JamID: 1, TeamID: 1, Title: `<script>alert("x")</script>`, ResultURL: "https://example.test/result", TeamName: "Team", Theme: "Theme"}}
	if err := tmpl.ExecuteTemplate(&output, "product_detail.html", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
		t.Fatal("product title was not safely escaped")
	}
	if !strings.Contains(output.String(), `rel="noopener noreferrer nofollow"`) {
		t.Fatal("external product link lacks safe rel attributes")
	}
}

func TestCanDiscloseProducts(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, Stage("unknown")} {
		if canDiscloseProducts(stage) {
			t.Fatalf("products disclosed at %q", stage)
		}
	}
	for _, stage := range []Stage{StageEvaluation, StageVoting, StageFinished} {
		if !canDiscloseProducts(stage) {
			t.Fatalf("products hidden at %q", stage)
		}
	}
}

func TestCanEditProductAuthorization(t *testing.T) {
	submission := StageSubmission
	team := productTeamRecord{CaptainID: 10, Override: stringStagePointer(submission)}
	if !canEditProduct(team, 10) {
		t.Fatal("captain must be allowed during submission")
	}
	team.CaptainID = 10
	team.ProductEditor = true
	if !canEditProduct(team, 20) {
		t.Fatal("appointed current editor must be allowed during submission")
	}
	team.ProductEditor = false
	if canEditProduct(team, 20) {
		t.Fatal("ordinary member must not edit the product")
	}
	evaluation := StageEvaluation
	team.Override = stringStagePointer(evaluation)
	team.CaptainID = 10
	if canEditProduct(team, 10) {
		t.Fatal("captain must not edit after submission")
	}
}

func stringStagePointer(stage Stage) *string {
	value := string(stage)
	return &value
}

func TestProductTemplatesExecuteAndHideNotes(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	product := ProductView{ID: 1, JamID: 2, TeamID: 3, TeamName: "Team", Title: "Result", ResultURL: "https://example.test/result", CommentaryURL: "https://example.test/review", Description: "Description", Notes: "PRIVATE NOTES", Status: "final", Theme: "Theme", NominationTitle: "PRIVATE NOMINATION RELATION"}
	tests := []struct {
		name string
		data any
	}{
		{"product_edit.html", productPageData{JamID: 2, TeamID: 3, TeamName: "Team", Product: product}},
		{"products_list.html", productsListPageData{JamID: 2, JamTitle: "Jam", Products: []ProductView{product}}},
		{"product_detail.html", productPageData{Product: product}},
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
			if tt.name != "product_edit.html" && strings.Contains(output.String(), product.Notes) {
				t.Fatal("public template disclosed private notes")
			}
			if tt.name != "product_edit.html" && strings.Contains(output.String(), product.NominationTitle) {
				t.Fatal("public product template disclosed nomination relationship")
			}
		})
	}
}

func TestProductsMigrationHasConcurrencyAndJamConstraints(t *testing.T) {
	body, err := os.ReadFile("../../migrations/003_products.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"UNIQUE (team_id, jam_id)", "FOREIGN KEY (team_id, jam_id)", "REFERENCES teams(id, jam_id)", "status = 'final'", "finalized_at IS NOT NULL", "char_length(title) >= 1", "char_length(result_url) >= 1"} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration lacks %q", required)
		}
	}
}

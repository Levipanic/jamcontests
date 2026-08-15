package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestValidateNominationTitle(t *testing.T) {
	if got := normalizeNominationTitle("  Приз архивистов  "); got != "Приз архивистов" {
		t.Fatalf("normalized title = %q", got)
	}
	if err := validateNominationTitle("Приз архивистов", false); err != nil {
		t.Fatalf("valid title rejected: %v", err)
	}
	if err := validateNominationTitle("", true); err != nil {
		t.Fatalf("optional empty title rejected: %v", err)
	}
	for _, value := range []string{"", "bad\ntitle", strings.Repeat("я", 161)} {
		if err := validateNominationTitle(value, false); err == nil {
			t.Fatalf("invalid title %q accepted", value)
		}
	}
}

func TestCanDiscloseNominations(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, StageEvaluation, Stage("unknown")} {
		if canDiscloseNominations(stage) {
			t.Fatalf("nominations disclosed at %q", stage)
		}
	}
	for _, stage := range []Stage{StageVoting, StageFinished} {
		if !canDiscloseNominations(stage) {
			t.Fatalf("nominations hidden at %q", stage)
		}
	}
}

func TestCanAdminMutateNominations(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, StageEvaluation} {
		if !canAdminMutateNominations(stage) {
			t.Fatalf("admin mutation unexpectedly closed at %q", stage)
		}
	}
	for _, stage := range []Stage{StageVoting, StageFinished, Stage("unknown")} {
		if canAdminMutateNominations(stage) {
			t.Fatalf("admin mutation unexpectedly open at %q", stage)
		}
	}
}

func TestNominationPublicTemplateDisclosure(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	nomination := NominationView{ID: 1, Kind: "team", Title: "Лучший сигнал", AuthorTeamName: "SECRET TEAM"}
	for _, tt := range []struct {
		stage      Stage
		wantAuthor bool
	}{
		{StageVoting, false},
		{StageFinished, true},
	} {
		var output bytes.Buffer
		data := nominationsPageData{JamID: 1, JamTitle: "Jam", Stage: tt.stage, Nominations: []NominationView{nomination}}
		if err := tmpl.ExecuteTemplate(&output, "nominations_list.html", data); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), nomination.AuthorTeamName) != tt.wantAuthor {
			t.Fatalf("author disclosure at %q does not match expected %v", tt.stage, tt.wantAuthor)
		}
	}
	var output bytes.Buffer
	curator := NominationView{Kind: "curator", Title: "Выбор куратора", AuthorTeamName: "FAKE AUTHOR"}
	data := nominationsPageData{JamID: 1, JamTitle: "Jam", Stage: StageFinished, Nominations: []NominationView{curator}}
	if err := tmpl.ExecuteTemplate(&output, "nominations_list.html", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), curator.AuthorTeamName) {
		t.Fatal("finished curator nomination rendered a fake author")
	}
}

func TestNominationTemplatesExecute(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	user := &User{ID: 1, Username: "admin", Role: "admin"}
	tests := []struct {
		name string
		data any
	}{
		{"nominations_list.html", nominationsPageData{JamID: 1, JamTitle: "Jam", Stage: StageVoting}},
		{"admin_nominations.html", adminNominationsPageData{PageData: PageData{User: user, CSRFToken: "token"}, Jam: &adminJam{ID: 1, Title: "Jam"}}},
	}
	for _, tt := range tests {
		var output bytes.Buffer
		if err := tmpl.ExecuteTemplate(&output, tt.name, tt.data); err != nil {
			t.Fatalf("execute %s: %v", tt.name, err)
		}
		if output.Len() == 0 {
			t.Fatalf("%s rendered no output", tt.name)
		}
	}
}

func TestNominationsMigrationHasSameJamAndHistoryConstraints(t *testing.T) {
	body, err := os.ReadFile("../../migrations/004_nominations.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	required := []string{
		"UNIQUE (id, jam_id)",
		"FOREIGN KEY (author_team_id, jam_id)",
		"REFERENCES teams(id, jam_id) ON DELETE RESTRICT",
		"FOREIGN KEY (product_id, jam_id)",
		"REFERENCES products(id, jam_id) ON DELETE RESTRICT",
		"FOREIGN KEY (product_id, author_team_id, jam_id)",
		"WHERE kind = 'team'",
		"withdrawn_at timestamptz",
	}
	for _, value := range required {
		if !strings.Contains(sql, value) {
			t.Errorf("migration lacks %q", value)
		}
	}
}

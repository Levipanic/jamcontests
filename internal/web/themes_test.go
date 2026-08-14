package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateThemePhrase(t *testing.T) {
	phrase, err := validateThemePhrase("  Тайный архив  ")
	if err != nil || phrase != "Тайный архив" {
		t.Fatalf("got %q, %v", phrase, err)
	}
	for _, value := range []string{"", "bad\nphrase", string(make([]byte, 161))} {
		if _, err := validateThemePhrase(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestCanDiscloseTeamTheme(t *testing.T) {
	if canDiscloseTeamTheme(StageUpcoming, true) {
		t.Fatal("theme must be hidden during upcoming")
	}
	if canDiscloseTeamTheme(StageSubmission, false) {
		t.Fatal("theme must be hidden from outsiders during submission")
	}
	if !canDiscloseTeamTheme(StageSubmission, true) || !canDiscloseTeamTheme(StageEvaluation, false) {
		t.Fatal("theme must be visible to submission members and everyone from evaluation")
	}
}

func TestHomeTemplateHidesThemesBeforeSubmission(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	data := PageData{Jam: &JamView{ID: 1, Title: "Jam", Stage: StageUpcoming}, Themes: []ThemeView{{ID: 1, Phrase: "SECRET THEME"}}}
	if err := tmpl.ExecuteTemplate(&output, "home.html", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "SECRET THEME") {
		t.Fatal("upcoming home disclosed a theme")
	}
}

package web

import (
	"bytes"
	"strings"
	"testing"
)

func renderHomeThemes(t *testing.T, data PageData) string {
	t.Helper()
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, "home.html", data); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func themeTestThemes() []ThemeView {
	return []ThemeView{{ID: 11, Phrase: "Тайный проспект"}, {ID: 12, Phrase: "Пыльный архив"}}
}

func TestThemeChoiceFormShownToEligibleCaptainInSubmission(t *testing.T) {
	body := renderHomeThemes(t, PageData{
		Jam:           &JamView{ID: 1, Title: "Jam", Stage: StageSubmission},
		Profile:       &ProfileView{TeamPublicID: "t1", IsCaptain: true, Eligible: true},
		Themes:        themeTestThemes(),
		SelectedTheme: &ThemeView{ID: 12, Phrase: "Пыльный архив"},
		CSRFToken:     "tok",
	})
	for _, want := range []string{
		`<form method="post" action="/teams/t1/theme" class="theme-choice-form"`,
		`<input type="radio" name="theme_id" value="11">`,
		`<input type="radio" name="theme_id" value="12" checked>`,
		"Отмечено печатью",
		"Изменить тему — приложить печать",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("theme choice form missing %q", want)
		}
	}
	for _, forbid := range []string{"Выбрано вашей командой", `<select name="theme_id"`} {
		if strings.Contains(body, forbid) {
			t.Fatalf("static selection leaked into captain form: %q", forbid)
		}
	}
}

func TestCaptainWithoutThemeKeepsSealUnchecked(t *testing.T) {
	body := renderHomeThemes(t, PageData{
		Jam:       &JamView{ID: 1, Title: "Jam", Stage: StageSubmission},
		Profile:   &ProfileView{TeamPublicID: "t1", IsCaptain: true, Eligible: true},
		Themes:    themeTestThemes(),
		CSRFToken: "tok",
	})
	if strings.Contains(body, "checked") {
		t.Fatal("no theme selected but a radio is pre-checked")
	}
	if !strings.Contains(body, "Отметьте тему на карточке, затем приложите печать подтверждения.") {
		t.Fatal("confirmation hint missing before any selection")
	}
	if !strings.Contains(body, "Приложить печать: подтвердить выбор") {
		t.Fatal("seal button label for first selection missing")
	}
}

func TestThemeCardsStaticForGuestAndOtherStages(t *testing.T) {
	body := renderHomeThemes(t, PageData{
		Jam:     &JamView{ID: 1, Title: "Jam", Stage: StageEvaluation},
		Themes:  themeTestThemes(),
		Profile: nil,
	})
	if strings.Contains(body, "name=\"theme_id\"") || strings.Contains(body, "theme-seal") {
		t.Fatal("guest sees a theme selection control")
	}
	for _, want := range []string{"Тайный проспект", "Пыльный архив"} {
		if !strings.Contains(body, want) {
			t.Fatalf("theme %q not rendered", want)
		}
	}
}

func TestThemeCardsForCaptainWithoutEligibility(t *testing.T) {
	body := renderHomeThemes(t, PageData{
		Jam:     &JamView{ID: 1, Title: "Jam", Stage: StageSubmission},
		Profile: &ProfileView{TeamPublicID: "t1", IsCaptain: true, Eligible: false},
		Themes:  themeTestThemes(),
	})
	if strings.Contains(body, "name=\"theme_id\"") || strings.Contains(body, "theme-seal") {
		t.Fatal("ineligible captain sees a theme selection control")
	}
	if !strings.Contains(body, "Выбор темы станет доступен после получения командой допуска.") {
		t.Fatal("eligibility note missing")
	}
}

func TestThemeOwnSelectionStampedForMember(t *testing.T) {
	body := renderHomeThemes(t, PageData{
		Jam:           &JamView{ID: 1, Title: "Jam", Stage: StageVoting},
		Profile:       &ProfileView{TeamPublicID: "t1", IsCaptain: false, Eligible: true},
		Themes:        themeTestThemes(),
		SelectedTheme: &ThemeView{ID: 11, Phrase: "Тайный проспект"},
	})
	if !strings.Contains(body, "Выбрано вашей командой") {
		t.Fatal("own selection stamp missing")
	}
	if strings.Contains(body, "name=\"theme_id\"") {
		t.Fatal("voting stage exposes a theme selection control")
	}
}

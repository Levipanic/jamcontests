package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestVotingStage(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, StageEvaluation, StageFinished, Stage("unknown")} {
		if canVote(stage) {
			t.Fatalf("voting open at %q", stage)
		}
	}
	if !canVote(StageVoting) {
		t.Fatal("voting closed during voting stage")
	}
}

func TestVotesMigrationHasSelectionAndSameJamConstraints(t *testing.T) {
	body, err := os.ReadFile("../../migrations/006_votes.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"PRIMARY KEY (user_id, nomination_id)",
		"REFERENCES users(id) ON DELETE RESTRICT",
		"FOREIGN KEY (nomination_id, jam_id)",
		"REFERENCES nominations(id, jam_id) ON DELETE RESTRICT",
		"FOREIGN KEY (product_id, jam_id)",
		"REFERENCES products(id, jam_id) ON DELETE RESTRICT",
		"nomination_votes_counts_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("votes migration lacks %q", required)
		}
	}
}

func TestVoteMutationUsesDatabaseRules(t *testing.T) {
	body, err := os.ReadFile("voting.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"ON CONFLICT (user_id, nomination_id) DO UPDATE",
		"RETURNING nomination_id, product_id",
		"nomination.withdrawn_at IS NULL",
		"product.status='final'",
		"FROM team_members",
		"status_override='voting'",
		"clock_timestamp() >= voting_starts_at AND clock_timestamp() < finishes_at",
		"FOR SHARE OF nomination, product, product_team, nomination_product",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("vote mutation lacks %q", required)
		}
	}
}

func TestVotingTemplateShowsBallotWithoutNominationAuthor(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	nomination := NominationView{
		ID: 8, Kind: "team", Title: "Лучший сигнал", AuthorTeamName: "HIDDEN AUTHOR",
		Products: []VotingProductView{
			{ID: 11, Title: "Открытый продукт", TeamName: "Команда A", VoteCount: 4, Selected: true},
			{ID: 12, Title: "Свой продукт", TeamName: "Команда B", VoteCount: 1, OwnProduct: true},
		},
	}
	var output bytes.Buffer
	data := nominationsPageData{User: &User{ID: 3}, CSRFToken: "token", JamID: 2, JamTitle: "Jam", Stage: StageVoting, Nominations: []NominationView{nomination}}
	if err = tmpl.ExecuteTemplate(&output, "nominations_list.html", data); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, required := range []string{
		`data-vote-nomination="8"`, `data-vote-product="11"`, `value="11" checked`,
		`data-vote-product="12"`, `value="12"  disabled`, `data-vote-count`,
		`/static/js/voting.js`, "Сохранить голос", "Открытый продукт", ">4</strong>",
	} {
		if !strings.Contains(html, required) {
			t.Errorf("voting ballot lacks %q", required)
		}
	}
	if strings.Contains(html, nomination.AuthorTeamName) {
		t.Fatal("voting ballot disclosed team nomination author")
	}
}

func TestVotingGuestCanSeeCountsButNotMutation(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	data := nominationsPageData{JamID: 2, JamTitle: "Jam", Stage: StageVoting, Nominations: []NominationView{{ID: 8, Title: "Choice", Products: []VotingProductView{{ID: 11, Title: "Product", VoteCount: 2}}}}}
	if err = tmpl.ExecuteTemplate(&output, "nominations_list.html", data); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	if !strings.Contains(html, ">2</strong>") || !strings.Contains(html, "Войти, чтобы голосовать") {
		t.Fatal("guest ballot does not expose public count and login action")
	}
	if strings.Contains(html, "data-vote-action") {
		t.Fatal("guest ballot rendered vote mutation control")
	}
}

func TestFinishedResultsTemplateShowsAllTiedWinnersAndZeroState(t *testing.T) {
	tmpl, err := loadTemplates("../../templates")
	if err != nil {
		t.Fatal(err)
	}
	nominations := []NominationView{
		{ID: 1, Kind: "team", Title: "Tie", AuthorTeamName: "Author Team", Result: &NominationResultView{TotalVotes: 4, Winners: []NominationWinnerView{
			{ProductID: 10, ProductTitle: "Winner A", TeamID: 20, TeamName: "Team A", VoteCount: 2},
			{ProductID: 11, ProductTitle: "Winner B", TeamID: 21, TeamName: "Team B", VoteCount: 2},
		}}},
		{ID: 2, Kind: "curator", Title: "No votes", Result: &NominationResultView{}},
	}
	var output bytes.Buffer
	if err = tmpl.ExecuteTemplate(&output, "nominations_list.html", nominationsPageData{JamID: 3, JamTitle: "Jam", Stage: StageFinished, Nominations: nominations}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, required := range []string{"Winner A", "Winner B", "Author Team", "Голосов: <strong>2</strong>", "Победитель не определён"} {
		if !strings.Contains(html, required) {
			t.Errorf("finished result lacks %q", required)
		}
	}
	for _, forbidden := range []string{"data-vote-action", "data-vote-product", "/static/js/voting.js", "первое место", "общий победитель"} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Errorf("finished result contains %q", forbidden)
		}
	}
}

func TestVotingJavaScriptUsesCSRFAndPolling(t *testing.T) {
	body, err := os.ReadFile("../../static/js/voting.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"/vote-counts", "/nominations/${ballot.dataset.voteNomination}/vote",
		`"X-CSRF-Token": csrf`, "JSON.stringify({ product_id:", "15000", "visibilitychange",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("voting JavaScript lacks %q", required)
		}
	}
}

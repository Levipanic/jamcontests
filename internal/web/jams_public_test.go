package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplyPublicJamScheduleUsesMoscowAndSuppressesOverrideTimer(t *testing.T) {
	schedule := Schedule{
		SubmissionStartsAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		EvaluationStartsAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		VotingStartsAt:     time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		FinishesAt:         time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	var automatic JamView
	applyPublicJamSchedule(&automatic, schedule, nil, time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC))
	if automatic.NextStageAt == nil || automatic.Dates[3].Moscow != "01.01.2026 15:00 МСК" {
		t.Fatalf("unexpected automatic public schedule: %+v", automatic)
	}
	override := string(StageVoting)
	var manual JamView
	applyPublicJamSchedule(&manual, schedule, &override, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if manual.Stage != StageVoting || manual.NextStageAt != nil || manual.NextStageRFC3339 != "" {
		t.Fatalf("override schedule exposed an automatic timer: %+v", manual)
	}
}

func TestCanUnpublishJam(t *testing.T) {
	for _, stage := range []Stage{StageUpcoming, StageSubmission, StageEvaluation, StageVoting} {
		if !canUnpublishJam(stage) {
			t.Fatalf("unpublish blocked at %q", stage)
		}
	}
	for _, stage := range []Stage{StageFinished, Stage("unknown")} {
		if canUnpublishJam(stage) {
			t.Fatalf("unpublish allowed at %q", stage)
		}
	}
}

func TestUnpublishRechecksFinishedWithDatabaseClock(t *testing.T) {
	body, err := os.ReadFile("jams_admin.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{"clock_timestamp() < finishes_at", "commandTag.RowsAffected() == 0", "status_override <> 'finished'"} {
		if !strings.Contains(source, required) {
			t.Errorf("unpublish guard lacks %q", required)
		}
	}
}

func TestPublicJamArchiveDisclosure(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, autoFinishedPublic := insertPublicTestJam(t, ctx, pool, "AUTO FINISHED", "published", nil,
		"2025-01-01T09:00:00Z", "2025-01-01T10:00:00Z", "2025-01-01T11:00:00Z", "2025-01-01T12:00:00Z")
	finished := "finished"
	_, overrideFinishedPublic := insertPublicTestJam(t, ctx, pool, "OVERRIDE FINISHED", "published", &finished,
		"2030-01-01T09:00:00Z", "2030-01-01T10:00:00Z", "2030-01-01T11:00:00Z", "2030-01-01T12:00:00Z")
	_, draftPublic := insertPublicTestJam(t, ctx, pool, "HIDDEN DRAFT", "draft", &finished,
		"2031-01-01T09:00:00Z", "2031-01-01T10:00:00Z", "2031-01-01T11:00:00Z", "2031-01-01T12:00:00Z")
	voting := "voting"
	insertPublicTestJam(t, ctx, pool, "REACTIVATED", "published", &voting,
		"2024-01-01T09:00:00Z", "2024-01-01T10:00:00Z", "2024-01-01T11:00:00Z", "2024-01-01T12:00:00Z")

	router := New(publicTestConfig(t), pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/archive", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("archive status = %d", recorder.Code)
	}
	html := recorder.Body.String()
	for _, visible := range []string{"AUTO FINISHED", "OVERRIDE FINISHED", "/jams/" + autoFinishedPublic, "/jams/" + overrideFinishedPublic} {
		if !strings.Contains(html, visible) {
			t.Errorf("archive lacks %q", visible)
		}
	}
	for _, hidden := range []string{"HIDDEN DRAFT", "REACTIVATED"} {
		if strings.Contains(html, hidden) {
			t.Errorf("archive disclosed %q", hidden)
		}
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/jams/"+autoFinishedPublic, nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "AUTO FINISHED") || !strings.Contains(recorder.Body.String(), "01.01.2025 15:00 МСК") {
		t.Fatalf("finished jam detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/jams/"+draftPublic, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("draft jam detail status = %d, want 404", recorder.Code)
	}
}

func publicTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Environment: "test", CSRFSecret: []byte("01234567890123456789012345678901"),
		SessionCookie: "test_session", SessionTTL: time.Hour,
		TemplatesDir: "../../templates", StaticDir: "../../static", AvatarDir: t.TempDir(), MaxAvatarBytes: 2 << 20,
	}
}

func insertPublicTestJam(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title, visibility string, override *string, submission, evaluation, voting, finishes string) (int64, string) {
	t.Helper()
	var id int64
	var publicID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jams (title, visibility, submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override, max_team_size)
		VALUES ($1, $2, $3::timestamptz, $4::timestamptz, $5::timestamptz, $6::timestamptz, $7, 5) RETURNING id, public_id`,
		title, visibility, submission, evaluation, voting, finishes, override).Scan(&id, &publicID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jam_themes (jam_id, phrase) VALUES ($1, $2)`, id, "Theme "+formatID(id)); err != nil {
		t.Fatal(err)
	}
	return id, publicID
}

package web

import (
	"bytes"
	"context"
	"image"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/testdb"
)

// journeyPostAvatarMultipart posts a form with one avatar file part.
func journeyPostAvatarMultipart(t *testing.T, router http.Handler, user *journeyUser, path, filename string, data []byte, form url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, values := range form {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	part, err := writer.CreateFormFile("avatar", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if user != nil {
		user.Jar.apply(request)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if user != nil {
		user.Jar.capture(recorder)
	}
	if recorder.Code != want {
		t.Fatalf("POST %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder
}

func TestAvatarUploadIsCompressedAndReplaced(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	insertJourneyAdmin(t, ctx, pool)
	cfg := publicTestConfig(t)
	router := New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	jamID, jamPublic := insertJourneyJam(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published' WHERE id=$1`, jamID); err != nil {
		t.Fatal(err)
	}
	captain := journeyUser{Jar: newJourneyJar()}
	journeyRegister(t, router, &captain, "avatarcaptain", "avatar-pass-123")
	journeyGet(t, router, &captain, "/jams/"+jamPublic+"/teams/new", http.StatusOK)

	bigAvatar := avatarFixturePNG(t, 1200, 900, false)
	createResponse := journeyPostAvatarMultipart(t, router, &captain, "/jams/"+jamPublic+"/teams/new", "big.png", bigAvatar, url.Values{
		"name": {"Avatar Team"}, "description": {"Команда с аватаром"}, "csrf_token": {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	teamPublic := journeyTeamPublicFromLocation(t, createResponse.Header().Get("Location"))

	var avatarPath string
	if err := pool.QueryRow(ctx, `SELECT avatar_path FROM teams WHERE public_id=$1`, teamPublic).Scan(&avatarPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(avatarPath, ".jpg") {
		t.Fatalf("stored avatar %q is not canonical jpg", avatarPath)
	}
	assertAvatarStored(t, cfg, avatarPath, bigAvatar)

	transparentAvatar := avatarFixturePNG(t, 500, 700, true)
	editResponse := journeyPostAvatarMultipart(t, router, &captain, "/teams/"+teamPublic+"/edit", "transparent.png", transparentAvatar, url.Values{
		"name": {"Avatar Team"}, "description": {"Команда с аватаром"}, "csrf_token": {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	if !strings.HasPrefix(editResponse.Header().Get("Location"), "/teams/"+teamPublic) {
		t.Fatalf("edit redirected to %q", editResponse.Header().Get("Location"))
	}
	var newAvatarPath string
	if err := pool.QueryRow(ctx, `SELECT avatar_path FROM teams WHERE public_id=$1`, teamPublic).Scan(&newAvatarPath); err != nil {
		t.Fatal(err)
	}
	if newAvatarPath == avatarPath {
		t.Fatal("avatar was not replaced")
	}
	if !strings.HasSuffix(newAvatarPath, ".png") {
		t.Fatalf("transparent avatar stored as %q, want .png", newAvatarPath)
	}
	assertAvatarStored(t, cfg, newAvatarPath, transparentAvatar)
	if _, err := os.Stat(filepath.Join(cfg.AvatarDir, avatarPath)); !os.IsNotExist(err) {
		t.Fatalf("replaced avatar %q still exists on disk", avatarPath)
	}
}

func TestAvatarUploadRejectsDecompressionBomb(t *testing.T) {
	pool := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	insertJourneyAdmin(t, ctx, pool)
	cfg := publicTestConfig(t)
	router := New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	jamID, jamPublic := insertJourneyJam(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE jams SET visibility='published' WHERE id=$1`, jamID); err != nil {
		t.Fatal(err)
	}
	captain := journeyUser{Jar: newJourneyJar()}
	journeyRegister(t, router, &captain, "bombcaptain", "bomb-pass-123")
	journeyGet(t, router, &captain, "/jams/"+jamPublic+"/teams/new", http.StatusOK)

	bomb := avatarFixtureHugePNG(t, 5000, 5000)
	response := journeyPostAvatarMultipart(t, router, &captain, "/jams/"+jamPublic+"/teams/new", "bomb.png", bomb, url.Values{
		"name": {"Bomb Team"}, "description": {"Команда-бомба"}, "csrf_token": {captain.Jar.csrfCookie()},
	}, http.StatusSeeOther)
	if !strings.Contains(response.Header().Get("Location"), "?error=") {
		t.Fatalf("bomb upload returned %q without error", response.Header().Get("Location"))
	}
	var teamCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM teams WHERE name='Bomb Team'`).Scan(&teamCount); err != nil {
		t.Fatal(err)
	}
	if teamCount != 0 {
		t.Fatal("team created despite rejected avatar")
	}
	entries, err := os.ReadDir(cfg.AvatarDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bomb upload left %d files on disk", len(entries))
	}
}

func assertAvatarStored(t *testing.T, cfg config.Config, name string, original []byte) {
	t.Helper()
	path := filepath.Join(cfg.AvatarDir, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxAvatarStoredBytes {
		t.Fatalf("stored avatar is %d bytes, cap is %d", info.Size(), maxAvatarStoredBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(data, original) {
		t.Fatal("raw upload stored unprocessed")
	}
	cfg_, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg_.Width > maxAvatarDimension || cfg_.Height > maxAvatarDimension {
		t.Fatalf("stored avatar is %dx%d", cfg_.Width, cfg_.Height)
	}
}

package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLogHandlerFormatFollowsEnvironment(t *testing.T) {
	cfg := config.Config{Environment: "production", LogLevel: slog.LevelInfo}
	if _, ok := logHandler(cfg, io.Discard).(*slog.JSONHandler); !ok {
		t.Fatal("production logging must be JSON")
	}
	cfg = config.Config{Environment: "development", LogLevel: slog.LevelDebug}
	if _, ok := logHandler(cfg, io.Discard).(*slog.TextHandler); !ok {
		t.Fatal("development logging must be text")
	}
}

func TestBuildServerHardensHTTPRuntime(t *testing.T) {
	cfg := serveTestConfig(t, "127.0.0.1:0")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := pgxpool.New(context.Background(), "postgres://unused@localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	server := buildServer(cfg, pool, logger)
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want 1 MiB", server.MaxHeaderBytes)
	}
	if server.ErrorLog == nil {
		t.Fatal("http.Server.ErrorLog must be connected to slog")
	}
	if server.Handler == nil {
		t.Fatal("handler must be built")
	}
	for _, setting := range []struct {
		name string
		got  time.Duration
	}{
		{"ReadHeaderTimeout", server.ReadHeaderTimeout},
		{"ReadTimeout", server.ReadTimeout},
		{"WriteTimeout", server.WriteTimeout},
		{"IdleTimeout", server.IdleTimeout},
	} {
		if setting.got <= 0 {
			t.Fatalf("%s must be positive, got %v", setting.name, setting.got)
		}
	}
}

func TestServeStartsReadiesAndShutsDownGracefully(t *testing.T) {
	pool := testdb.Open(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	cfg := serveTestConfig(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- serve(ctx, cfg, pool, logger) }()

	deadline := time.Now().Add(10 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		client := &http.Client{Timeout: time.Second}
		response, err := client.Get("http://" + addr + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("server did not become ready on /health")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve returned %v after graceful shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
}

func serveTestConfig(t *testing.T, addr string) config.Config {
	t.Helper()
	return config.Config{
		Environment: "test", HTTPAddr: addr,
		DatabaseURL:   "postgres://unused@localhost/unused",
		CSRFSecret:    []byte("01234567890123456789012345678901"),
		SessionCookie: "test_session", SessionTTL: time.Hour,
		MigrationsDir: "../../migrations", TemplatesDir: "../../templates",
		StaticDir: "../../static", AvatarDir: t.TempDir(), MaxAvatarBytes: 2 << 20,
		LogLevel: slog.LevelInfo,
	}
}

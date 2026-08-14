package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/Levipanic/jamcontests/internal/config"
	"github.com/Levipanic/jamcontests/internal/database"
	"github.com/Levipanic/jamcontests/internal/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(context.Background(), os.Args[1:], logger); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("использование: jamcontests <serve|migrate|create-admin>")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("конфигурация: %w", err)
	}
	databaseURL := cfg.DatabaseURL
	if args[0] == "migrate" {
		databaseURL = cfg.MigrationDatabaseURL
	}
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch args[0] {
	case "migrate":
		if len(args) != 1 {
			return errors.New("использование: jamcontests migrate")
		}
		migrationCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := database.Migrate(migrationCtx, pool, cfg.MigrationsDir); err != nil {
			return err
		}
		logger.Info("migrations applied")
		return nil
	case "create-admin":
		flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		username := flags.String("username", "", "public username")
		email := flags.String("email", "", "optional email")
		passwordEnv := flags.String("password-env", "ADMIN_PASSWORD", "environment variable containing the password")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *username == "" {
			return errors.New("использование: jamcontests create-admin --username NAME [--email EMAIL] [--password-env ADMIN_PASSWORD]")
		}
		password := os.Getenv(*passwordEnv)
		if password == "" {
			return fmt.Errorf("переменная окружения %s с паролем не задана", *passwordEnv)
		}
		adminCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := web.CreateAdmin(adminCtx, pool, *username, *email, password); err != nil {
			return err
		}
		logger.Info("administrator account is ready", "username", *username)
		return nil
	case "serve":
		if len(args) != 1 {
			return errors.New("использование: jamcontests serve")
		}
		return serve(ctx, cfg, pool, logger)
	default:
		return fmt.Errorf("неизвестная команда %q; ожидается serve, migrate или create-admin", args[0])
	}
}

func serve(parent context.Context, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) error {
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           web.New(cfg, pool, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("остановить HTTP-сервер: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		logger.Info("HTTP server stopped")
		return nil
	}
}

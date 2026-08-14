package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Environment          string
	HTTPAddr             string
	DatabaseURL          string
	MigrationDatabaseURL string
	CSRFSecret           []byte
	SessionCookie        string
	SessionTTL           time.Duration
	MigrationsDir        string
	TemplatesDir         string
	StaticDir            string
	AvatarDir            string
	MaxAvatarBytes       int64
}

func Load() (Config, error) {
	ttl, err := time.ParseDuration(env("SESSION_TTL", "720h"))
	if err != nil || ttl <= 0 {
		return Config{}, errors.New("SESSION_TTL must be a positive duration")
	}

	maxAvatarBytes, err := strconv.ParseInt(env("MAX_AVATAR_BYTES", "2097152"), 10, 64)
	if err != nil || maxAvatarBytes <= 0 {
		return Config{}, errors.New("MAX_AVATAR_BYTES must be a positive integer")
	}

	cfg := Config{
		Environment:          env("APP_ENV", "development"),
		HTTPAddr:             env("HTTP_ADDR", ":8080"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		MigrationDatabaseURL: os.Getenv("MIGRATION_DATABASE_URL"),
		CSRFSecret:           []byte(os.Getenv("CSRF_SECRET")),
		SessionCookie:        env("SESSION_COOKIE", "jamcontests_session"),
		SessionTTL:           ttl,
		MigrationsDir:        env("MIGRATIONS_DIR", "migrations"),
		TemplatesDir:         env("TEMPLATES_DIR", "templates"),
		StaticDir:            env("STATIC_DIR", "static"),
		AvatarDir:            env("AVATAR_DIR", "storage/avatars"),
		MaxAvatarBytes:       maxAvatarBytes,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.MigrationDatabaseURL == "" {
		cfg.MigrationDatabaseURL = cfg.DatabaseURL
	}
	if len(cfg.CSRFSecret) < 32 {
		return Config{}, fmt.Errorf("CSRF_SECRET must contain at least 32 bytes")
	}
	if cfg.Environment != "development" && cfg.Environment != "production" && cfg.Environment != "test" {
		return Config{}, errors.New("APP_ENV must be development, production, or test")
	}
	return cfg, nil
}

func (c Config) Production() bool {
	return c.Environment == "production"
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
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
	LogLevel             slog.Level
	TrustedProxies       []string
}

// LoadServe loads the runtime configuration for the serve and create-admin
// commands. Migration owner credentials are deliberately not required here.
func LoadServe() (Config, error) {
	cfg, err := loadBase()
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if len(cfg.CSRFSecret) < 32 {
		return Config{}, fmt.Errorf("CSRF_SECRET must contain at least 32 bytes")
	}
	return cfg, nil
}

// LoadMigrate loads the configuration for the migrate command. In production
// the migration owner role must be explicit; the runtime role may not own the
// schema. The development fallback keeps local setups convenient.
func LoadMigrate() (Config, error) {
	cfg, err := loadBase()
	if err != nil {
		return Config{}, err
	}
	if cfg.MigrationDatabaseURL == "" {
		if cfg.Environment != "development" {
			return Config{}, errors.New("MIGRATION_DATABASE_URL is required outside development; use a separate migration owner role in production")
		}
		cfg.MigrationDatabaseURL = cfg.DatabaseURL
	}
	return cfg, nil
}

func loadBase() (Config, error) {
	ttl, err := time.ParseDuration(env("SESSION_TTL", "720h"))
	if err != nil || ttl <= 0 {
		return Config{}, errors.New("SESSION_TTL must be a positive duration")
	}

	maxAvatarBytes, err := strconv.ParseInt(env("MAX_AVATAR_BYTES", "2097152"), 10, 64)
	if err != nil || maxAvatarBytes <= 0 {
		return Config{}, errors.New("MAX_AVATAR_BYTES must be a positive integer")
	}

	logLevel, err := parseLogLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	trustedProxies, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, err
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
		LogLevel:             logLevel,
		TrustedProxies:       trustedProxies,
	}
	if cfg.Environment != "development" && cfg.Environment != "production" && cfg.Environment != "test" {
		return Config{}, errors.New("APP_ENV must be development, production, or test")
	}
	return cfg, nil
}

func (c Config) Production() bool {
	return c.Environment == "production"
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("LOG_LEVEL must be debug, info, warn, or error")
	}
}

// parseTrustedProxies parses a comma-separated list of IP addresses or CIDR
// networks. An empty value trusts no proxies; forwarded headers are then
// ignored and ClientIP resolves to the direct peer.
func parseTrustedProxies(value string) ([]string, error) {
	var proxies []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err == nil {
			proxies = append(proxies, addr.String())
			continue
		}
		prefix, prefixErr := netip.ParsePrefix(entry)
		if prefixErr != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES contains invalid address %q", entry)
		}
		proxies = append(proxies, prefix.String())
	}
	return proxies, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

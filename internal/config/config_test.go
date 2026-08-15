package config

import (
	"log/slog"
	"os"
	"testing"
)

func TestLoadServeRequiresDatabaseAndSecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CSRF_SECRET", "01234567890123456789012345678901")
	t.Setenv("MIGRATION_DATABASE_URL", "")
	if _, err := LoadServe(); err == nil {
		t.Fatal("LoadServe accepted a missing DATABASE_URL")
	}

	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	t.Setenv("CSRF_SECRET", "short")
	if _, err := LoadServe(); err == nil {
		t.Fatal("LoadServe accepted a short CSRF_SECRET")
	}
}

func TestLoadMigrateRequiresOwnerOutsideDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	t.Setenv("CSRF_SECRET", "01234567890123456789012345678901")
	t.Setenv("MIGRATION_DATABASE_URL", "")
	if _, err := LoadMigrate(); err == nil {
		t.Fatal("LoadMigrate fell back to the runtime role in production")
	}

	t.Setenv("MIGRATION_DATABASE_URL", "postgres://owner@localhost/db")
	cfg, err := LoadMigrate()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MigrationDatabaseURL != "postgres://owner@localhost/db" {
		t.Fatalf("MigrationDatabaseURL = %q", cfg.MigrationDatabaseURL)
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("MIGRATION_DATABASE_URL", "")
	cfg, err = LoadMigrate()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MigrationDatabaseURL != cfg.DatabaseURL {
		t.Fatal("development migrate did not fall back to DATABASE_URL")
	}
}

func TestLoadRejectsInvalidEnvironmentValues(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	t.Setenv("CSRF_SECRET", "01234567890123456789012345678901")
	t.Setenv("MIGRATION_DATABASE_URL", "postgres://owner@localhost/db")

	for _, test := range []struct {
		key, value string
	}{
		{"APP_ENV", "staging"},
		{"SESSION_TTL", "-1h"},
		{"MAX_AVATAR_BYTES", "0"},
		{"LOG_LEVEL", "verbose"},
		{"TRUSTED_PROXIES", "999.999.1.1"},
		{"TRUSTED_PROXIES", "10.0.0.0/99"},
	} {
		t.Setenv(test.key, test.value)
		if _, err := LoadServe(); err == nil {
			t.Fatalf("LoadServe accepted %s=%q", test.key, test.value)
		}
		t.Setenv(test.key, "")
	}
}

func TestLoadParsesLogLevelAndTrustedProxies(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	t.Setenv("CSRF_SECRET", "01234567890123456789012345678901")
	t.Setenv("MIGRATION_DATABASE_URL", "postgres://owner@localhost/db")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1, 10.0.0.0/8,2001:db8::1")
	cfg, err := LoadServe()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	want := []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::1"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	for i := range want {
		if cfg.TrustedProxies[i] != want[i] {
			t.Fatalf("TrustedProxies[%d] = %q, want %q", i, cfg.TrustedProxies[i], want[i])
		}
	}
}

func TestLoadServeIgnoresMissingMigrationURL(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	t.Setenv("CSRF_SECRET", "01234567890123456789012345678901")
	t.Setenv("MIGRATION_DATABASE_URL", "")
	if _, err := LoadServe(); err != nil {
		t.Fatalf("serve must not require migration owner credentials: %v", err)
	}
}

func TestEnvFallback(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	if got := env("LOG_LEVEL", "info"); got != "info" {
		t.Fatalf("env fallback = %q", got)
	}
	os.Unsetenv("APP_ENV")
	if got := env("APP_ENV", "development"); got != "development" {
		t.Fatalf("env fallback = %q", got)
	}
}

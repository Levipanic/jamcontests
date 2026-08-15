package web

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Levipanic/jamcontests/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateAdmin promotes an existing username or creates a new administrator.
// Existing account credentials are never overwritten.
func CreateAdmin(ctx context.Context, pool *pgxpool.Pool, username, rawEmail, password string) error {
	username = strings.TrimSpace(username)
	email, err := validateIdentity(username, rawEmail)
	if err != nil {
		return err
	}
	if len(password) < 10 || len(password) > 1024 {
		return errors.New("пароль должен содержать от 10 до 1024 байт")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminRoleLock); err != nil {
		return fmt.Errorf("заблокировать роли администраторов: %w", err)
	}
	var adminCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&adminCount); err != nil {
		return fmt.Errorf("проверить существующих администраторов: %w", err)
	}

	var userID int64
	var existingEmail *string
	var role string
	err = tx.QueryRow(ctx, `SELECT id, email, role FROM users WHERE lower(username) = lower($1) FOR UPDATE`, username).Scan(&userID, &existingEmail, &role)
	if err == nil {
		if email != nil && (existingEmail == nil || !strings.EqualFold(*email, *existingEmail)) {
			return errors.New("существующий пользователь имеет другой email; данные аккаунта не изменены")
		}
		if role == "admin" {
			return tx.Commit(ctx)
		}
		if adminCount > 0 {
			return errors.New("первый администратор уже существует; меняйте роли через защищенную /admin")
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET role = 'admin', updated_at = now() WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("назначить администратора: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("отозвать сессии после назначения: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_audit_log (admin_user_id, action, entity_type, entity_id, reason, before_data, after_data)
			VALUES ($1, 'admin.bootstrap_promote', 'user', $1, 'CLI bootstrap promotion',
			        jsonb_build_object('role', $2::text), jsonb_build_object('role', 'admin'))`, userID, role); err != nil {
			return fmt.Errorf("записать аудит назначения: %w", err)
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return fmt.Errorf("найти пользователя: %w", err)
	}
	if adminCount > 0 {
		return errors.New("первый администратор уже существует; создавайте пользователей через регистрацию и меняйте роли через /admin")
	}

	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("проверить пароль: %w", err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, $3, 'admin') RETURNING id`, username, email, passwordHash).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			return errors.New("имя пользователя или email уже заняты")
		}
		return fmt.Errorf("создать администратора: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_log (admin_user_id, action, entity_type, entity_id, reason, after_data)
		VALUES ($1, 'admin.bootstrap_create', 'user', $1, 'CLI bootstrap creation', jsonb_build_object('role', 'admin'))`, userID); err != nil {
		return fmt.Errorf("записать аудит создания: %w", err)
	}
	return tx.Commit(ctx)
}

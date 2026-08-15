# Jam Contests

Легковесная платформа командных творческих джемов на Go, Gin и PostgreSQL.

## Локальный запуск

Требуются Go 1.24+, Docker с Compose и PowerShell 7+.

1. Запустите PostgreSQL:

   ```powershell
   docker compose up -d postgres
   ```

2. Задайте переменные окружения по образцу `.env.example`. Минимум:

   ```powershell
   $env:DATABASE_URL = 'postgres://jamcontests:jamcontests@localhost:5432/jamcontests?sslmode=disable'
   $env:CSRF_SECRET = 'replace-with-at-least-32-random-characters'
   ```

3. Примените миграции:

   ```powershell
   go run ./cmd/jamcontests migrate
   ```

4. Создайте первого администратора:

   ```powershell
   $env:ADMIN_PASSWORD = 'use-a-long-unique-password'
   go run ./cmd/jamcontests create-admin --username admin
   Remove-Item Env:ADMIN_PASSWORD
   ```

5. Запустите сервер:

   ```powershell
   go run ./cmd/jamcontests serve
   ```

Сайт будет доступен на `http://localhost:8080`. Ссылки на административную панель в пользовательском интерфейсе нет; защищённый вход находится по адресу `/admin`.

## Проверки

```powershell
go fmt ./...
go test ./...
go build ./...
go vet ./...
```

PostgreSQL-интеграционные тесты используют отдельную случайную схему и пропускаются, если `TEST_DATABASE_URL` не задан. Для запуска с локальной БД из Compose:

```powershell
$env:TEST_DATABASE_URL = 'postgres://jamcontests:jamcontests@localhost:5432/jamcontests?sslmode=disable'
go test ./...
Remove-Item Env:TEST_DATABASE_URL
```

Указанная роль должна иметь право создавать и удалять схемы. Каждый тест применяет актуальные миграции в собственной схеме и удаляет её после завершения.

Загруженные аватары сохраняются в `storage/avatars` и не попадают в Git. В production этот каталог и PostgreSQL должны входить в резервное копирование.

В production задайте отдельный `MIGRATION_DATABASE_URL` для роли-владельца схемы; без него команда `migrate` не запускается, а `serve` эту переменную не читает. Runtime-роль из `DATABASE_URL` должна иметь только необходимые приложению права; для `admin_audit_log` ей нужны `SELECT` и `INSERT`, но не `UPDATE`, `DELETE`, `TRUNCATE` или владение таблицей. Миграция `009_runtime_privileges.sql` выдаёт права runtime-роли, если она существует, а миграции дополнительно блокируют изменения аудита триггерами. Инструкция по развёртыванию — в `docs/production.md`, шаблоны ролей и systemd-юнит — в `deploy/`.

`GET /health` отдаёт `200` только когда PostgreSQL отвечает и схема полностью промигрирована; см. `docs/production.md`.

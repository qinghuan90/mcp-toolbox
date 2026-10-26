package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type DB struct {
	*sql.DB
	Path string
}

type Result struct{ sql.Result }

func (r Result) RowsAffected() int64 {
	n, _ := r.Result.RowsAffected()
	return n
}

type Tx struct{ *sql.Tx }

var parameters = regexp.MustCompile(`\$([1-9][0-9]*)`)

func query(s string) string {
	return parameters.ReplaceAllString(s, "?$1")
}

func normalizeArgs(args []any) []any {
	out := make([]any, len(args))
	for i, value := range args {
		switch v := value.(type) {
		case time.Time:
			out[i] = v.UTC().Format(time.RFC3339Nano)
		case *time.Time:
			if v != nil {
				out[i] = v.UTC().Format(time.RFC3339Nano)
			}
		default:
			out[i] = value
		}
	}
	return out
}

func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("SQLITE_PATH is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(abs); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("SQLite path must be a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 11 {
		db.Close()
		return nil, fmt.Errorf("unsupported SQLite schema version %d", version)
	}
	if version == 0 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		if _, err = tx.Exec(schema); err == nil {
			_, err = tx.Exec("PRAGMA user_version=11")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("SQLite schema: %w", err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version == 1 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		if _, err = tx.Exec("ALTER TABLE discoveries ADD COLUMN scanned_at DATETIME NOT NULL DEFAULT ''"); err == nil {
			_, err = tx.Exec("UPDATE discoveries SET scanned_at=CURRENT_TIMESTAMP WHERE scanned_at=''")
		}
		if err == nil {
			_, err = tx.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(2)")
		}
		if err == nil {
			_, err = tx.Exec("PRAGMA user_version=2")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("SQLite migration 2: %w", err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version == 1 || version == 2 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, target := range []struct{ table, column string }{
			{"users", "locked_until"}, {"sessions", "expires_at"}, {"tokens", "expires_at"}, {"tokens", "created_at"}, {"tokens", "last_seen_at"},
			{"instances", "created_at"}, {"logs", "created_at"}, {"tasks", "created_at"}, {"discoveries", "scanned_at"}, {"management_audit", "created_at"}, {"schema_migrations", "applied_at"},
		} {
			if err = canonicalizeColumn(tx, target.table, target.column); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite time migration %s.%s: %w", target.table, target.column, err)
			}
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(3)"); err == nil {
			_, err = tx.Exec("PRAGMA user_version=3")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 3 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		if _, err = tx.Exec("ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0"); err == nil {
			_, err = tx.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(4)")
		}
		if err == nil {
			_, err = tx.Exec("PRAGMA user_version=4")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("SQLite migration 4: %w", err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 4 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		if _, err = tx.Exec("CREATE TABLE scan_roots(path TEXT PRIMARY KEY,created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)"); err == nil {
			_, err = tx.Exec("CREATE TRIGGER audit_scan_roots_insert AFTER INSERT ON scan_roots BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('scan_roots',new.path,'INSERT'); END")
		}
		if err == nil {
			_, err = tx.Exec("CREATE TRIGGER audit_scan_roots_delete AFTER DELETE ON scan_roots BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('scan_roots',old.path,'DELETE'); END")
		}
		if err == nil {
			_, err = tx.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(5)")
		}
		if err == nil {
			_, err = tx.Exec("PRAGMA user_version=5")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("SQLite migration 5: %w", err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 5 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		if _, err = tx.Exec("CREATE TABLE system_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)"); err == nil {
			_, err = tx.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(6)")
		}
		if err == nil {
			_, err = tx.Exec("PRAGMA user_version=6")
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("SQLite migration 6: %w", err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 6 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, statement := range []string{
			"CREATE TABLE ip_rules(id TEXT PRIMARY KEY,cidr TEXT NOT NULL,effect TEXT NOT NULL CHECK(effect IN ('allow','deny')),created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(cidr,effect))",
			"CREATE TRIGGER audit_ip_rules_insert AFTER INSERT ON ip_rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('ip_rules',new.id,'INSERT'); END",
			"CREATE TRIGGER audit_ip_rules_delete AFTER DELETE ON ip_rules BEGIN INSERT INTO management_audit(entity_type,entity_id,action) VALUES('ip_rules',old.id,'DELETE'); END",
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(7)",
			"PRAGMA user_version=7",
		} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite migration 7: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 7 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, statement := range []string{
			"ALTER TABLE tokens ADD COLUMN client_deleted INTEGER NOT NULL DEFAULT 0",
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(8)",
			"PRAGMA user_version=8",
		} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite migration 8: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 8 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, statement := range []string{
			"CREATE TABLE access_roles(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,description TEXT NOT NULL DEFAULT '',version INTEGER NOT NULL DEFAULT 1)",
			"CREATE TABLE role_instances(role_id TEXT NOT NULL REFERENCES access_roles(id) ON DELETE CASCADE,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,PRIMARY KEY(role_id,instance_id))",
			"CREATE TABLE user_roles(role_id TEXT NOT NULL REFERENCES access_roles(id) ON DELETE CASCADE,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,PRIMARY KEY(role_id,user_id))",
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(9)",
			"PRAGMA user_version=9",
		} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite migration 9: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 9 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, statement := range []string{
			"ALTER TABLE access_roles ADD COLUMN all_mcp INTEGER NOT NULL DEFAULT 0",
			"CREATE TABLE role_tools(role_id TEXT NOT NULL,instance_id TEXT NOT NULL,tool_name TEXT NOT NULL CHECK(length(tool_name)>0),PRIMARY KEY(role_id,instance_id,tool_name),FOREIGN KEY(role_id,instance_id) REFERENCES role_instances(role_id,instance_id) ON DELETE CASCADE)",
			"INSERT INTO role_tools(role_id,instance_id,tool_name) SELECT role_id,instance_id,'*' FROM role_instances",
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(10)",
			"PRAGMA user_version=10",
		} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite migration 10: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version <= 10 {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		for _, statement := range []string{
			"ALTER TABLE tokens ADD COLUMN never_expires INTEGER NOT NULL DEFAULT 0",
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(11)",
			"PRAGMA user_version=11",
		} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				db.Close()
				return nil, fmt.Errorf("SQLite migration 11: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	var check string
	if err = db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		db.Close()
		return nil, fmt.Errorf("SQLite integrity check failed: %s %v", check, err)
	}
	foreignRows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		db.Close()
		return nil, err
	}
	if foreignRows.Next() {
		foreignRows.Close()
		db.Close()
		return nil, errors.New("SQLite foreign key integrity check failed")
	}
	if err = foreignRows.Err(); err != nil {
		foreignRows.Close()
		db.Close()
		return nil, err
	}
	foreignRows.Close()
	if err = validateSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(abs, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{DB: db, Path: abs}, nil
}

func validateSchema(db *sql.DB) error {
	tables := map[string][]string{
		"schema_migrations": {"version", "applied_at"},
		"users":             {"id", "username", "password_hash", "role", "enabled", "failures", "locked_until", "must_change_password"},
		"sessions":          {"hash", "user_id", "expires_at"},
		"tokens":            {"id", "user_id", "client_name", "hash", "expires_at", "revoked", "created_at", "last_seen_at", "client_deleted", "never_expires"},
		"instances":         {"id", "name", "transport", "config", "secret", "version", "enabled", "status", "last_error", "created_at", "runtime_generation"},
		"config_versions":   {"instance_id", "version", "config", "secret"},
		"rules":             {"id", "instance_id", "tool_name", "effect"},
		"ip_rules":          {"id", "cidr", "effect", "created_at"},
		"logs":              {"id", "user_id", "token_id", "client_name", "instance_id", "tool_name", "status", "detail", "duration_ms", "created_at", "runtime_generation"},
		"tasks":             {"id", "kind", "status", "detail", "result", "created_at"},
		"discoveries":       {"id", "source", "name", "config", "secret", "fingerprint", "warnings", "importable", "imported_instance_id", "scanned_at"},
		"scan_roots":        {"path", "created_at"},
		"system_settings":   {"key", "value"},
		"access_roles":      {"id", "name", "description", "version", "all_mcp"},
		"role_instances":    {"role_id", "instance_id"},
		"role_tools":        {"role_id", "instance_id", "tool_name"},
		"user_roles":        {"role_id", "user_id"},
		"management_audit":  {"id", "entity_type", "entity_id", "action", "created_at"},
	}
	for table, expected := range tables {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var def sql.NullString
			if err = rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			found[name] = true
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, column := range expected {
			if !found[column] {
				return fmt.Errorf("SQLite schema missing %s.%s", table, column)
			}
		}
	}
	for _, name := range []string{"audit_users_insert", "audit_users_update", "audit_users_delete", "audit_tokens_insert", "audit_tokens_update", "audit_tokens_delete", "audit_rules_insert", "audit_rules_update", "audit_rules_delete", "audit_instances_insert", "audit_instances_update", "audit_instances_delete", "audit_scan_roots_insert", "audit_scan_roots_delete"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("SQLite audit trigger missing: %s", name)
		}
	}
	for _, name := range []string{"logs_user_time", "logs_uncertain_generation"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("SQLite index missing: %s", name)
		}
	}
	return nil
}

// RecoverPending rolls back a restore interrupted before its durable commit.
// It is called before opening the database on process startup.
func RecoverPending(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	marker := abs + ".restore.pending"
	value, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	id := string(value)
	if len(id) != 32 {
		return errors.New("invalid pending restore marker")
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return errors.New("invalid pending restore marker")
		}
	}
	backup := abs + ".restore.rollback-" + id
	if _, err = os.Stat(backup); err == nil {
		if _, currentErr := os.Stat(abs); currentErr == nil {
			failed := abs + ".restore.interrupted-" + id
			if err = os.Rename(abs, failed); err != nil {
				return err
			}
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if removeErr := os.Remove(abs + suffix); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
		}
		if err = os.Rename(backup, abs); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err = os.Stat(abs); err != nil {
		return errors.New("pending restore has no recoverable database")
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(abs))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (d *DB) Ping(ctx context.Context) error { return d.PingContext(ctx) }
func (d *DB) Exec(ctx context.Context, statement string, args ...any) (Result, error) {
	r, err := d.ExecContext(ctx, query(statement), normalizeArgs(args)...)
	return Result{r}, err
}
func (d *DB) Query(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(ctx, query(statement), normalizeArgs(args)...)
}
func (d *DB) QueryRow(ctx context.Context, statement string, args ...any) *sql.Row {
	return d.QueryRowContext(ctx, query(statement), normalizeArgs(args)...)
}
func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx}, nil
}
func (t *Tx) Exec(ctx context.Context, statement string, args ...any) (Result, error) {
	r, err := t.ExecContext(ctx, query(statement), normalizeArgs(args)...)
	return Result{r}, err
}
func (t *Tx) QueryRow(ctx context.Context, statement string, args ...any) *sql.Row {
	return t.QueryRowContext(ctx, query(statement), normalizeArgs(args)...)
}
func (t *Tx) Rollback(_ context.Context) error { return t.Tx.Rollback() }
func (t *Tx) Commit(_ context.Context) error   { return t.Tx.Commit() }

// Backup uses SQLite's online backup primitive, so WAL contents are included.
func (d *DB) Backup(ctx context.Context, destination string) error {
	if destination == "" {
		return errors.New("empty backup destination")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("backup destination exists")
	}
	quoted := strings.ReplaceAll(destination, "'", "''")
	_, err := d.DB.ExecContext(ctx, "VACUUM INTO '"+quoted+"'")
	if err != nil {
		return err
	}
	return os.Chmod(destination, 0600)
}

func (d *DB) Count(ctx context.Context, table string) (int64, error) {
	allowed := map[string]bool{"users": true, "sessions": true, "tokens": true, "instances": true, "config_versions": true, "rules": true, "logs": true, "tasks": true, "discoveries": true, "management_audit": true}
	if !allowed[table] {
		return 0, errors.New("invalid table")
	}
	var n int64
	err := d.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n)
	return n, err
}

func Version(ctx context.Context, d *DB) (int, error) {
	var version int
	err := d.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

func canonicalizeColumn(tx *sql.Tx, table, column string) error {
	rows, err := tx.Query("SELECT rowid,CAST(" + column + " AS TEXT) FROM " + table + " WHERE " + column + " IS NOT NULL")
	if err != nil {
		return err
	}
	type change struct {
		id    int64
		value string
	}
	var updates []change
	for rows.Next() {
		var id int64
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		if raw == "" {
			continue
		}
		value, parseErr := parseTime(raw)
		if parseErr != nil {
			rows.Close()
			return parseErr
		}
		updates = append(updates, change{id, value.UTC().Format(time.RFC3339Nano)})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range updates {
		if _, err = tx.Exec("UPDATE "+table+" SET "+column+"=? WHERE rowid=?", u.value, u.id); err != nil {
			return err
		}
	}
	return nil
}

func parseTime(raw string) (time.Time, error) {
	raw = strings.Split(raw, " m=")[0]
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999 -0700", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unrecognized stored time")
}

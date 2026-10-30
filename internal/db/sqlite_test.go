package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Opens a file backed store the way the panel does at startup
func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database = config.DatabaseConfig{
		Path:            path,
		AutoMigrate:     true,
		MaxConnections:  25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 300,
	}
	store, err := NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func sqlHandle(t *testing.T, store *Store) *sql.DB {
	t.Helper()
	sqlDB, err := store.DB().DB()
	if err != nil {
		t.Fatal(err)
	}
	return sqlDB
}

// DSN pragmas must reach every pooled connection, not just the first
func TestStorePragmasOnEveryConnection(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "discopanel.db"))
	sqlDB := sqlHandle(t, store)
	ctx := context.Background()

	// Holding conns open forces the pool to dial fresh ones
	var conns []*sql.Conn
	for range 4 {
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})

	want := map[string]string{
		"foreign_keys": "1",
		"journal_mode": "wal",
		"busy_timeout": "5000",
		"synchronous":  "1", // NORMAL
	}
	for i, conn := range conns {
		for pragma, expected := range want {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("conn %d: PRAGMA %s: %v", i, pragma, err)
			}
			if got != expected {
				t.Errorf("conn %d: PRAGMA %s = %q, want %q", i, pragma, got, expected)
			}
		}
	}
}

func TestFreshDatabaseMigratesWithoutDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discopanel.db")
	store := openTestStore(t, path)
	if drift := store.SchemaDrift(); len(drift) > 0 {
		t.Fatalf("fresh database drifted from models: %v", drift)
	}

	dir, err := migrationDir()
	if err != nil {
		t.Fatal(err)
	}
	files, err := dir.Files()
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := sqlHandle(t, store).QueryRow(`SELECT COUNT(*) FROM "` + revisionTable + `"`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Errorf("revision rows = %d, want %d migration files", applied, len(files))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening an up to date database is a no-op
	again := openTestStore(t, path)
	if drift := again.SchemaDrift(); len(drift) > 0 {
		t.Fatalf("reopened database drifted: %v", drift)
	}
}

func TestForeignKeyViolationRejected(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "discopanel.db"))
	session, err := gorm.Open(sqlite.Dialector{Conn: sqlHandle(t, store)}, &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE fk_parent (id integer PRIMARY KEY)",
		"CREATE TABLE fk_child (id integer PRIMARY KEY, parent_id integer NOT NULL REFERENCES fk_parent(id))",
		"INSERT INTO fk_parent (id) VALUES (1)",
		"INSERT INTO fk_child (id, parent_id) VALUES (1, 1)",
	} {
		if err := session.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = session.Exec("INSERT INTO fk_child (id, parent_id) VALUES (2, 99)").Error
	if !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Fatalf("orphan insert error = %v, want foreign key violation", err)
	}
	err = session.Exec("INSERT INTO fk_parent (id) VALUES (1)").Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate insert error = %v, want duplicated key", err)
	}
}

// Rows written before the driver swap used this layout, new rows must match
// so text comparisons and ordering stay consistent across both
func TestTimestampsKeepStoredLayout(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "discopanel.db"))
	ctx := context.Background()
	at := time.Date(2026, 3, 4, 5, 6, 7, 891000000, time.UTC)

	fresh := &v1.ApiToken{Id: "new", Name: "new", UserId: "u", TokenHash: "h1", CreatedAt: timestamppb.New(at)}
	if err := store.DB().WithContext(ctx).Create(fresh).Error; err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := store.DB().Raw("SELECT CAST(created_at AS TEXT) FROM api_tokens WHERE id = ?", "new").Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if want := "2026-03-04 05:06:07.891+00:00"; raw != want {
		t.Errorf("stored created_at = %q, want %q", raw, want)
	}

	// Row as the previous cgo driver left it
	legacy := "2025-12-31 23:59:58.123456789+00:00"
	if err := store.DB().Exec("INSERT INTO api_tokens (id, name, user_id, token_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		"old", "old", "u", "h2", legacy).Error; err != nil {
		t.Fatal(err)
	}
	var old v1.ApiToken
	if err := store.DB().First(&old, "id = ?", "old").Error; err != nil {
		t.Fatal(err)
	}
	if got, want := old.CreatedAt.AsTime(), time.Date(2025, 12, 31, 23, 59, 58, 123456789, time.UTC); !got.Equal(want) {
		t.Errorf("legacy created_at = %v, want %v", got, want)
	}

	var newest string
	if err := store.DB().Raw("SELECT id FROM api_tokens ORDER BY created_at DESC LIMIT 1").Scan(&newest).Error; err != nil {
		t.Fatal(err)
	}
	if newest != "new" {
		t.Errorf("newest token = %q, want new", newest)
	}
}

// Pre migration backups and support bundles snapshot with VACUUM INTO
func TestVacuumIntoSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, filepath.Join(dir, "discopanel.db"))
	copyPath := filepath.Join(dir, "snapshot.db")
	if _, err := sqlHandle(t, store).Exec("VACUUM INTO ?", copyPath); err != nil {
		t.Fatalf("VACUUM INTO: %v", err)
	}
	snap, err := sql.Open(sqlite.DriverName, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	tables, err := tableNames(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{revisionTable, "servers", "roles"} {
		if !tables[name] {
			t.Errorf("snapshot missing table %s", name)
		}
	}
}

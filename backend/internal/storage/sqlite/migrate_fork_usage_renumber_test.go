package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// TestMigrateRepairsForkUsageRenumbering replicates the field profile from the
// fork's installed builds: a database that applied the fork's usage migrations
// at their original numbers (0129-0133) before upstream claimed those numbers.
// goose then sees 129-133 as applied and skips upstream's 0129-0133, and would
// re-run the renumbered 0136-0140 bodies over the already-present schema,
// aborting on 0136's duplicate CREATE INDEX. prepareForkUsageMigrationHistory
// must replay upstream's missing effects and record 0136-0140 as applied.
func TestMigrateRepairsForkUsageRenumbering(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ao.db")+pragmas)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	upTo(t, db, 128)
	applyLegacyForkUsageMigrations(t, db)

	if err := migrate(db); err != nil {
		t.Fatalf("migrate fork-burn database: %v", err)
	}

	// Every version 129-140 is recorded, including the renumbered usage
	// migrations 0136-0140 that prepareForkUsageMigrationHistory recorded.
	assertAppliedMigrations(t, db, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140)

	// Upstream's 0129-0133 physical effects are present despite goose skipping
	// those migrations because their numbers were already recorded.
	var changeLogIndex int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_change_log_created_at_seq'`,
	).Scan(&changeLogIndex); err != nil {
		t.Fatal(err)
	}
	if changeLogIndex != 1 {
		t.Fatalf("change_log retention index = %d, want 1", changeLogIndex)
	}
	assertColumnPresent(t, db, "pr", "review_partial")
	assertColumnPresent(t, db, "conversations", "opencode_mode")
	assertColumnPresent(t, db, "shell_terminals", "transient")

	// The fork's usage schema is intact.
	for _, table := range []string{"usage_bindings", "usage_sources", "model_usage_events", "model_usage_event_timing"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("usage table %s count = %d, want 1", table, n)
		}
	}
	assertColumnPresent(t, db, "model_usage_events", "cache_creation_input_tokens")
	assertTableSQLContains(t, db, "usage_sources", "'acp_usage'")
	assertTableSQLContains(t, db, "usage_bindings", "length(trim(harness)) > 0")
	assertTableSQLContains(t, db, "model_usage_events", "'acp'")

	// A second startup is a no-op: the repaired database migrates cleanly.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate pass: %v", err)
	}
}

// TestMigrateLeavesFreshDatabaseUntouched guards the fresh-database path: a
// schema with no usage tables must let goose apply every migration normally.
func TestMigrateLeavesFreshDatabaseUntouched(t *testing.T) {
	db := openMigratedTestDB(t)
	assertAppliedMigrations(t, db, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140)
	assertColumnPresent(t, db, "conversations", "opencode_mode")
	assertColumnPresent(t, db, "shell_terminals", "transient")
}

// applyLegacyForkUsageMigrations replays the fork's usage migrations at their
// original numbers on top of 0128, exactly as the fork's shipped builds
// applied them.
func applyLegacyForkUsageMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	legacyFS := fstest.MapFS{}
	for _, migration := range []struct {
		version       int64
		legacyName    string
		canonicalPath string
	}{
		{129, "usage_summary_range_index.sql", "migrations/0136_usage_summary_range_index.sql"},
		{130, "usage_event_timing.sql", "migrations/0137_usage_event_timing.sql"},
		{131, "usage_cache_creation_split.sql", "migrations/0138_usage_cache_creation_split.sql"},
		{132, "usage_acp_source.sql", "migrations/0139_usage_acp_source.sql"},
		{133, "reconcile_acp_usage_schema.sql", "migrations/0140_reconcile_acp_usage_schema.sql"},
	} {
		contents, err := migrationsFS.ReadFile(migration.canonicalPath)
		if err != nil {
			t.Fatalf("read canonical migration %q: %v", migration.canonicalPath, err)
		}
		legacyFS[fmt.Sprintf("migrations/%04d_%s", migration.version, migration.legacyName)] = &fstest.MapFile{Data: contents}
	}

	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(legacyFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatalf("apply legacy fork usage migrations: %v", err)
	}
}

func assertColumnPresent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&n); err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	if n != 1 {
		t.Fatalf("%s.%s count = %d, want 1", table, column, n)
	}
}

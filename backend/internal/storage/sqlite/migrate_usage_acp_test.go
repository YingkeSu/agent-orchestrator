package sqlite

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyACP139UpgradesWithoutLosingUsage(t *testing.T) {
	db := openMigratedTestDB(t)
	downTo(t, db, 138)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, display_name, registered_at)
VALUES ('cache-split-project', '/tmp/cache-split-project', 'cache-split-project', CURRENT_TIMESTAMP);
INSERT INTO sessions (
    id, project_id, num, harness, activity_last_at, workspace_path, branch, created_at, updated_at
)
VALUES (
    'cache-split-session', 'cache-split-project', 1, 'claude-code', CURRENT_TIMESTAMP,
    '/tmp/cache-split-session', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
);
INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at)
VALUES ('cache-split-session', 'claude-code', 'native-root', 'active', CURRENT_TIMESTAMP);
INSERT INTO usage_sources (binding_id, kind, artifact_path, state, updated_at)
VALUES (last_insert_rowid(), 'claude_main', '/tmp/claude.jsonl', 'active', CURRENT_TIMESTAMP);
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, model_id, usage_measurement_kind,
    input_tokens, uncached_input_tokens, cache_creation_input_tokens, source_event_key
)
SELECT binding_id, id, 'anthropic', 'claude-x', 'native_reported', 20, 15, 5, 'cache-split-ok'
FROM usage_sources WHERE artifact_path = '/tmp/claude.jsonl';`); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile("testdata/0139_usage_acp_source_legacy.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(legacy), "-- +goose Down")[0]
	if _, err := db.Exec(up); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (139, 1)"); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 140)
	var preserved int64
	if err := db.QueryRow("SELECT cache_creation_input_tokens FROM model_usage_events WHERE source_event_key='cache-split-ok'").Scan(&preserved); err != nil || preserved != 5 {
		t.Fatalf("preserved cache tokens = %d, error %v", preserved, err)
	}
	var schema string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name='model_usage_events'").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schema, "'acp'") {
		t.Fatal("legacy 139 still rejects ACP event provider")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign keys broken by 140")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

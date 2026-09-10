package sqlite

import "testing"

func TestACPUsageMigrationPreservesUsageAndForeignKeys(t *testing.T) {
	db := openMigratedTestDB(t)
	downTo(t, db, 131)
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
	upTo(t, db, 132)
	var tokens int64
	if err := db.QueryRow("SELECT cache_creation_input_tokens FROM model_usage_events WHERE source_event_key = 'cache-split-ok'").Scan(&tokens); err != nil || tokens != 5 {
		t.Fatalf("preserved cache creation = %d, err %v", tokens, err)
	}
	if _, err := db.Exec(`INSERT INTO usage_bindings (session_id, harness, native_root_id, state, updated_at)
 VALUES ('cache-split-session', 'opencode', 'acp-root', 'active', CURRENT_TIMESTAMP);
 INSERT INTO usage_sources (binding_id, kind, artifact_path, state, updated_at)
 VALUES (last_insert_rowid(), 'acp_usage', 'conversation:acp-root', 'active', CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left dangling foreign keys")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

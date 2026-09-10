package sqlite

import (
	"testing"
)

// The 0131 column pins both the non-negative floor and the additive
// cache_creation <= uncached_input invariant in SQL (ADR 0006 sketch open
// question 1: verified accepted and enforced under ADD COLUMN on the bundled
// driver). This keeps the pin honest: a violating write must fail at the
// schema level, not only at the Go write path.
func TestMigrateUsageCacheCreationSplitCHECK(t *testing.T) {
	db := openMigratedTestDB(t)

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
		t.Fatalf("valid write rejected: %v", err)
	}

	if _, err := db.Exec(`
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, model_id, usage_measurement_kind,
    input_tokens, uncached_input_tokens, cache_creation_input_tokens, source_event_key
)
SELECT binding_id, id, 'anthropic', 'claude-x', 'native_reported', 20, 15, 16, 'cache-split-over'
FROM usage_sources WHERE artifact_path = '/tmp/claude.jsonl';`); err == nil {
		t.Fatal("cache_creation above uncached_input was accepted")
	}

	if _, err := db.Exec(`
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, model_id, usage_measurement_kind,
    input_tokens, uncached_input_tokens, cache_creation_input_tokens, source_event_key
)
SELECT binding_id, id, 'anthropic', 'claude-x', 'native_reported', 20, 15, -1, 'cache-split-negative'
FROM usage_sources WHERE artifact_path = '/tmp/claude.jsonl';`); err == nil {
		t.Fatal("negative cache_creation was accepted")
	}
}

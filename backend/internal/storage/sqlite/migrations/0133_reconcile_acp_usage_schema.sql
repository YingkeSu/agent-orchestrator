-- Reconcile databases that already ran the fork's earlier 0132 schema.
-- Rebuilds preserve all usage, timing references, cursors, and indexes.
-- Summary: certify ACP-delivered usage as its own source kind.
--
-- Worker sessions driven through a chat provider (the ACP layer) already
-- persist per-turn token accounting as kind="usage" rows in
-- conversation_provider_events, but the V1 usage pipeline only certified
-- transcript-file sources, so worker usage never reached the usage statistics.
-- Three CHECK allowlists gate the pipeline's storage, and SQLite cannot widen
-- a CHECK in place: all three tables rebuild here (the 0117 precedent).
--
-- 1. usage_sources.kind gains 'acp_usage': the durable source is not a
--    provider-owned transcript but AO's own conversation_provider_events
--    archive, scanned through the same write-once event contract
--    (ApplyUsageChunk, UNIQUE(binding_id, source_event_key) dedupe, CAS
--    cursor), so replay-safe backfill of historical worker usage needs no new
--    event storage.
-- 2. usage_bindings.harness drops its enum CHECK. ACP bindings carry the chat
--    session's real harness (opencode, and any future chat-capable harness),
--    so the per-harness session usage summary attributes usage truthfully.
--    Enumerating 26+ harness values in SQL would force a rebuild per new
--    harness; Go owns the valid values (domain.AgentHarness) and every writer
--    derives the harness from the sessions row.
-- 3. model_usage_events.provider_id gains 'acp': ACP usage arrives already
--    provider-neutral (input tokens disjoint from a folded cache-read+write
--    bucket), which is its own vocabulary, not a mislabeled anthropic/openai
--    shape. provider_usage_json stores the verbatim ACP usage object.
-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_sources_cdc_update;

CREATE TABLE model_usage_events_next (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id                  INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    usage_source_id             INTEGER NOT NULL REFERENCES usage_sources (id) ON DELETE CASCADE,
    provider_id                 TEXT NOT NULL CHECK (provider_id IN ('openai', 'anthropic', 'acp')),
    billing_provider_id         TEXT CHECK (billing_provider_id IS NULL OR trim(billing_provider_id) <> ''),
    model_id                    TEXT NOT NULL CHECK (trim(model_id) <> ''),
    usage_measurement_kind      TEXT NOT NULL
        CHECK (usage_measurement_kind IN ('native_reported', 'ao_estimated', 'mixed', 'unknown')),
    input_tokens                INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    cached_input_tokens         INTEGER CHECK (cached_input_tokens IS NULL OR cached_input_tokens >= 0),
    uncached_input_tokens       INTEGER CHECK (uncached_input_tokens IS NULL OR uncached_input_tokens >= 0),
    output_tokens               INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    provider_usage_json         TEXT
        CHECK (provider_usage_json IS NULL
               OR (json_valid(provider_usage_json) AND json_type(provider_usage_json, '$') = 'object')),
    source_event_key            TEXT NOT NULL CHECK (trim(source_event_key) <> ''),
    created_at                  TIMESTAMP,
    input_cost_nanos            INTEGER CHECK (input_cost_nanos IS NULL OR input_cost_nanos >= 0),
    cached_input_cost_nanos     INTEGER CHECK (cached_input_cost_nanos IS NULL OR cached_input_cost_nanos >= 0),
    output_cost_nanos           INTEGER CHECK (output_cost_nanos IS NULL OR output_cost_nanos >= 0),
    estimated_cost_nanos        INTEGER CHECK (estimated_cost_nanos IS NULL OR estimated_cost_nanos >= 0),
    pricing_version             TEXT NOT NULL DEFAULT '',
    -- 0116 and 0131 appended these columns physically last; the rebuild
    -- preserves that layout so no SELECT * consumer or pinned column-order
    -- test shifts.
    billing_provider_source     TEXT CHECK (billing_provider_source IS NULL OR billing_provider_source IN ('observed', 'inferred')),
    cache_creation_input_tokens INTEGER
        CHECK (cache_creation_input_tokens IS NULL
               OR (cache_creation_input_tokens >= 0
                   AND (uncached_input_tokens IS NULL
                        OR cache_creation_input_tokens <= uncached_input_tokens))),
    UNIQUE (binding_id, source_event_key),
    CHECK (input_tokens IS NULL OR cached_input_tokens IS NULL OR uncached_input_tokens IS NULL
           OR input_tokens = cached_input_tokens + uncached_input_tokens)
);

INSERT INTO model_usage_events_next
SELECT id, binding_id, usage_source_id, provider_id, billing_provider_id,
       model_id, usage_measurement_kind,
       input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
       provider_usage_json, source_event_key, created_at,
       input_cost_nanos, cached_input_cost_nanos, output_cost_nanos,
       estimated_cost_nanos, pricing_version, billing_provider_source,
       cache_creation_input_tokens
FROM model_usage_events;

DROP TABLE model_usage_events;
ALTER TABLE model_usage_events_next RENAME TO model_usage_events;

CREATE INDEX idx_model_usage_events_binding_model ON model_usage_events (binding_id, model_id);
CREATE INDEX idx_model_usage_events_usage_source ON model_usage_events (usage_source_id);
CREATE INDEX idx_model_usage_events_cost_candidates
    ON model_usage_events (billing_provider_id, pricing_version, id)
    WHERE estimated_cost_nanos IS NULL;
CREATE INDEX idx_model_usage_events_canonical_cost_candidates
    ON model_usage_events (
        CASE lower(trim(billing_provider_id))
            WHEN 'z.ai' THEN 'zai'
            ELSE lower(trim(billing_provider_id))
        END,
        id
    )
    WHERE billing_provider_id IS NOT NULL
      AND estimated_cost_nanos IS NULL;
CREATE INDEX idx_model_usage_events_open_attribution
    ON model_usage_events (usage_source_id, id)
    WHERE billing_provider_id IS NULL OR billing_provider_source = 'inferred';
CREATE INDEX idx_model_usage_events_created_at ON model_usage_events (created_at);

CREATE TABLE usage_sources_next (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    binding_id          INTEGER NOT NULL REFERENCES usage_bindings (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('claude_main', 'claude_subagent', 'codex_rollout', 'kimi_wire', 'acp_usage')),
    native_session_id   TEXT NOT NULL DEFAULT '',
    subagent_id         TEXT NOT NULL DEFAULT '',
    artifact_path       TEXT NOT NULL CHECK (trim(artifact_path) <> ''),
    file_identity       TEXT NOT NULL DEFAULT '',
    generation          INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    byte_offset         INTEGER NOT NULL DEFAULT 0 CHECK (byte_offset >= 0),
    parser_state_json   TEXT NOT NULL DEFAULT '{}',
    state               TEXT NOT NULL CHECK (state IN ('pending', 'active', 'complete', 'error')),
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    anomaly_count       INTEGER NOT NULL DEFAULT 0 CHECK (anomaly_count >= 0),
    next_retry_at       TIMESTAMP,
    last_error_code     TEXT NOT NULL DEFAULT '',
    updated_at          TIMESTAMP NOT NULL,
    UNIQUE (binding_id, artifact_path, generation)
);

INSERT INTO usage_sources_next
SELECT id, binding_id, kind, native_session_id, subagent_id, artifact_path,
       file_identity, generation, byte_offset, parser_state_json, state,
       failure_count, anomaly_count, next_retry_at, last_error_code, updated_at
FROM usage_sources;

DROP TABLE usage_sources;
ALTER TABLE usage_sources_next RENAME TO usage_sources;

CREATE INDEX idx_usage_sources_state_retry ON usage_sources (state, next_retry_at);
CREATE INDEX idx_usage_sources_binding_kind ON usage_sources (binding_id, kind);
CREATE INDEX idx_usage_sources_codex_native_latest
    ON usage_sources (kind, native_session_id, binding_id, generation DESC, id DESC);

CREATE TABLE usage_bindings_next (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    harness            TEXT NOT NULL CHECK (length(trim(harness)) > 0),
    native_root_id     TEXT NOT NULL CHECK (trim(native_root_id) <> ''),
    initial_model_id   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('discovering', 'active', 'finalizing', 'complete', 'partial')),
    last_error_code    TEXT NOT NULL DEFAULT '',
    updated_at         TIMESTAMP NOT NULL,
    provider_hint      TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, harness, native_root_id)
);

INSERT INTO usage_bindings_next
SELECT id, session_id, harness, native_root_id, initial_model_id,
       state, last_error_code, updated_at, provider_hint
FROM usage_bindings;

DROP TABLE usage_bindings;
ALTER TABLE usage_bindings_next RENAME TO usage_bindings;

CREATE INDEX idx_usage_bindings_session_state ON usage_bindings (session_id, state);

CREATE TRIGGER usage_bindings_cdc_insert AFTER INSERT ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_update AFTER UPDATE ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_sources_cdc_update AFTER UPDATE ON usage_sources
WHEN OLD.anomaly_count IS NOT NEW.anomaly_count
  OR OLD.last_error_code IS NOT NEW.last_error_code
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ub.session_id, 'session_updated', json_object('id', ub.session_id), NEW.updated_at
    FROM usage_bindings ub JOIN sessions s ON s.id = ub.session_id WHERE ub.id = NEW.binding_id;
END;

COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- Intentionally retain the widened allowlists on rollback; removing them would
-- destroy certified ACP facts. Migration 0132 is the same target schema.
SELECT 1;

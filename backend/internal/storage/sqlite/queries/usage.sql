-- name: UpsertUsageBinding :one
INSERT INTO usage_bindings (
    session_id, harness, native_root_id, initial_model_id, state,
    last_error_code, updated_at, provider_hint
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, harness, native_root_id) DO UPDATE SET
    initial_model_id = CASE
        WHEN excluded.initial_model_id <> '' THEN excluded.initial_model_id
        ELSE usage_bindings.initial_model_id
    END,
    provider_hint = CASE
        WHEN excluded.provider_hint <> '' THEN excluded.provider_hint
        ELSE usage_bindings.provider_hint
    END,
    state = CASE
        WHEN usage_bindings.state IN ('finalizing', 'complete', 'partial')
          AND excluded.state IN ('discovering', 'active')
        THEN usage_bindings.state
        ELSE excluded.state
    END,
    last_error_code = CASE
        WHEN usage_bindings.last_error_code = 'codex_source_budget_exceeded'
        THEN usage_bindings.last_error_code
        WHEN usage_bindings.state IN ('finalizing', 'complete', 'partial')
          AND excluded.state IN ('discovering', 'active')
        THEN usage_bindings.last_error_code
        ELSE excluded.last_error_code
    END,
    updated_at = excluded.updated_at
RETURNING *;

-- name: GetUsageBindingBySessionHarnessRoot :one
SELECT *
FROM usage_bindings
WHERE session_id = ? AND harness = ? AND native_root_id = ?;

-- name: ListUsageBindingsForSession :many
SELECT *
FROM usage_bindings
WHERE session_id = ?
ORDER BY updated_at, id;

-- name: FinalizeUsageBindingsForSessionLaunch :many
UPDATE usage_bindings
SET state = 'finalizing',
    last_error_code = CASE
        WHEN usage_bindings.last_error_code = 'codex_source_budget_exceeded'
        THEN usage_bindings.last_error_code
        ELSE ''
    END,
    updated_at = sqlc.arg(finalized_at)
WHERE usage_bindings.session_id = sqlc.arg(session_id)
  AND EXISTS (
      SELECT 1
      FROM sessions
      WHERE sessions.id = usage_bindings.session_id
        AND sessions.runtime_launch_id = sqlc.arg(expected_runtime_launch_id)
        AND sessions.updated_at = sqlc.arg(expected_session_updated_at)
        AND sessions.is_terminated = 0
  )
RETURNING *;

-- name: InsertUsageSource :one
INSERT INTO usage_sources (
    binding_id, kind, native_session_id, subagent_id, artifact_path,
    file_identity, generation, byte_offset, parser_state_json,
    state, failure_count, anomaly_count, next_retry_at, last_error_code,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (binding_id, artifact_path, generation) DO UPDATE SET
    native_session_id = CASE
        WHEN excluded.native_session_id <> '' THEN excluded.native_session_id
        ELSE usage_sources.native_session_id
    END,
    subagent_id = CASE
        WHEN excluded.subagent_id <> '' THEN excluded.subagent_id
        ELSE usage_sources.subagent_id
    END,
    updated_at = excluded.updated_at
RETURNING *;

-- name: ListUsageSourcesForBinding :many
SELECT *
FROM usage_sources
WHERE binding_id = ?
ORDER BY generation, id;

-- name: ListWatchableUsageSources :many
-- acp_usage sources are table-backed, not transcript files: the file watcher
-- would only fail to open their artifact path. The ACP certifier owns them.
SELECT us.*
FROM usage_sources us
JOIN usage_bindings ub ON ub.id = us.binding_id
JOIN sessions s ON s.id = ub.session_id
WHERE us.kind <> 'acp_usage'
  AND (s.is_terminated = 0 OR ub.state = 'finalizing')
  AND NOT (
      us.state = 'complete'
      AND us.last_error_code = 'artifact_replaced'
  )
  AND us.id = (
      SELECT latest.id
      FROM usage_sources latest
      WHERE latest.binding_id = us.binding_id
        AND latest.artifact_path = us.artifact_path
      ORDER BY latest.generation DESC, latest.id DESC
      LIMIT 1
  )
ORDER BY us.artifact_path, us.generation, us.id;

-- name: HasPendingUsageDiscovery :one
SELECT CAST(EXISTS (
    SELECT 1
    FROM usage_bindings ub
    JOIN sessions s ON s.id = ub.session_id
    WHERE (s.is_terminated = 0 OR ub.state = 'finalizing')
      AND ub.harness IN ('claude-code', 'codex', 'kimi')
      AND (
          ub.harness = 'kimi'
          OR ub.state = 'discovering'
          OR ub.last_error_code = 'source_discovery_pending'
          OR EXISTS (
              SELECT 1
              FROM usage_codex_pending_children pending
              WHERE pending.binding_id = ub.id
          )
          OR EXISTS (
              SELECT 1
              FROM usage_sources source
              WHERE source.binding_id = ub.id
                AND source.state = 'error'
                AND source.last_error_code IN ('artifact_missing', 'source_read_failed')
                AND source.id = (
                    SELECT latest.id
                    FROM usage_sources latest
                    WHERE latest.binding_id = source.binding_id
                      AND latest.artifact_path = source.artifact_path
                    ORDER BY latest.generation DESC, latest.id DESC
                    LIMIT 1
                )
          )
      )
) AS INTEGER);

-- name: ListLatestRetiredCodexReplacementClaimsByPath :many
SELECT us.*
FROM usage_bindings ub
JOIN sessions s ON s.id = ub.session_id
JOIN usage_sources us ON us.id = (
    SELECT latest.id
    FROM usage_sources latest
    WHERE latest.binding_id = ub.id
      AND latest.artifact_path = sqlc.arg(artifact_path)
    ORDER BY latest.generation DESC, latest.id DESC
    LIMIT 1
)
WHERE us.kind = 'codex_rollout'
  AND us.state = 'complete'
  AND us.last_error_code = 'artifact_replaced'
  AND (s.is_terminated = 0 OR ub.state = 'finalizing')
ORDER BY us.binding_id, us.generation, us.id;

-- name: ListUsageDiscoveryBindings :many
SELECT ub.*
FROM usage_bindings ub
JOIN sessions s ON s.id = ub.session_id
WHERE (s.is_terminated = 0 OR ub.state = 'finalizing')
  AND ub.harness IN ('claude-code', 'codex', 'kimi')
  AND (
      ub.state IN ('discovering', 'active', 'finalizing')
      OR (ub.state = 'partial' AND ub.last_error_code = 'codex_source_budget_exceeded')
  )
  AND (
      ub.harness IN ('claude-code', 'kimi')
      OR ub.state = 'discovering'
      OR ub.state = 'finalizing'
      OR ub.last_error_code = 'codex_source_budget_exceeded'
      OR ub.last_error_code = 'source_discovery_pending'
      OR NOT EXISTS (
          SELECT 1
          FROM usage_sources us
          WHERE us.binding_id = ub.id
            AND us.kind = 'codex_rollout'
      )
      OR EXISTS (
          SELECT 1
          FROM usage_sources us
          WHERE us.binding_id = ub.id
            AND us.kind = 'codex_rollout'
            AND us.state = 'error'
            AND us.last_error_code IN ('artifact_missing', 'source_read_failed')
      )
	  OR EXISTS (
	      SELECT 1
	      FROM usage_codex_pending_children
	      WHERE binding_id = ub.id
	  )
  )
ORDER BY ub.updated_at, ub.id
LIMIT ?;

-- name: ListUsageBindingsForCodexParent :many
SELECT DISTINCT ub.*
FROM usage_bindings ub
JOIN sessions s ON s.id = ub.session_id
JOIN usage_sources parent ON parent.binding_id = ub.id
WHERE (s.is_terminated = 0 OR ub.state = 'finalizing')
  AND ub.harness = 'codex'
  AND (
      ub.state IN ('discovering', 'active', 'finalizing')
      OR (ub.state = 'partial' AND ub.last_error_code = 'codex_source_budget_exceeded')
  )
  AND parent.kind = 'codex_rollout'
  AND parent.native_session_id = sqlc.arg(parent_native_session_id)
  AND parent.id = (
      SELECT latest.id
      FROM usage_sources latest
      WHERE latest.binding_id = parent.binding_id
        AND latest.kind = 'codex_rollout'
        AND latest.native_session_id = parent.native_session_id
      ORDER BY latest.generation DESC, latest.id DESC
      LIMIT 1
  )
ORDER BY ub.updated_at, ub.id;

-- name: GetUsageSourceWithBindingAndSession :one
SELECT
    us.id AS source_id,
    us.binding_id,
    us.kind,
    us.native_session_id,
    us.subagent_id,
    us.artifact_path,
    us.file_identity,
    us.generation,
    us.byte_offset,
    us.parser_state_json,
    us.state AS source_state,
    us.failure_count,
    us.anomaly_count,
    us.next_retry_at,
    us.last_error_code AS source_last_error_code,
    us.updated_at AS source_updated_at,
    ub.session_id,
    ub.harness,
    ub.native_root_id,
    ub.initial_model_id,
    ub.provider_hint,
    ub.state AS binding_state
FROM usage_sources us
JOIN usage_bindings ub ON ub.id = us.binding_id
WHERE us.id = ?;

-- name: UpdateUsageSourceCursor :exec
UPDATE usage_sources SET
    byte_offset = ?,
    parser_state_json = ?,
    state = ?,
    failure_count = ?,
    anomaly_count = ?,
    next_retry_at = ?,
    last_error_code = ?,
    updated_at = ?
WHERE id = ?;

-- name: UpdateUsageSourceLifecycle :execrows
UPDATE usage_sources SET
    state = sqlc.arg(state),
    failure_count = COALESCE(sqlc.narg(failure_count), failure_count),
    last_error_code = sqlc.arg(last_error_code),
    next_retry_at = sqlc.narg(next_retry_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateUsageBinding :execrows
UPDATE usage_bindings SET
    state = CASE
        WHEN sqlc.arg(state) = '' THEN usage_bindings.state
        ELSE sqlc.arg(state)
    END,
    last_error_code = CASE
        WHEN usage_bindings.last_error_code = 'codex_source_budget_exceeded'
        THEN usage_bindings.last_error_code
        ELSE sqlc.arg(last_error_code)
    END,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: CompleteUsageBindingIfSettled :execrows
UPDATE usage_bindings
SET state = CASE
        WHEN usage_bindings.last_error_code = 'codex_source_budget_exceeded'
          OR EXISTS (
            SELECT 1
            FROM usage_sources
            WHERE usage_sources.binding_id = sqlc.arg(usage_binding_id)
              AND last_error_code <> 'artifact_replaced'
              AND (anomaly_count > 0 OR last_error_code <> '')
        ) THEN 'partial'
        ELSE 'complete'
    END,
    last_error_code = CASE
        WHEN usage_bindings.last_error_code = 'codex_source_budget_exceeded'
        THEN usage_bindings.last_error_code
        ELSE ''
    END,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(usage_binding_id)
  AND state = 'finalizing'
  AND EXISTS (
      SELECT 1
      FROM usage_sources
      WHERE usage_sources.binding_id = sqlc.arg(usage_binding_id)
  )
  AND NOT EXISTS (
      SELECT 1
      FROM usage_sources
      WHERE usage_sources.binding_id = sqlc.arg(usage_binding_id)
        AND state <> 'complete'
	)
	AND (
	    usage_bindings.last_error_code = 'codex_source_budget_exceeded'
	    OR NOT EXISTS (
	        SELECT 1
	        FROM usage_codex_pending_children
	        WHERE binding_id = sqlc.arg(usage_binding_id)
	    )
	)
	AND NOT EXISTS (
	    SELECT 1
	    FROM usage_codex_source_discovery malformed
	    WHERE malformed.binding_id = sqlc.arg(usage_binding_id)
	      AND malformed.source_id = (
	          SELECT latest.id
	          FROM usage_sources latest
	          WHERE latest.binding_id = malformed.binding_id
	            AND latest.kind = 'codex_rollout'
	            AND latest.native_session_id = malformed.native_session_id
	          ORDER BY latest.generation DESC, latest.id DESC
	          LIMIT 1
	      )
	      AND malformed.has_mixed_child_types = 1
  );

-- name: GetModelUsageEventByKey :one
SELECT
    event.id, event.usage_source_id, event.provider_id, event.billing_provider_id,
    event.billing_provider_source, event.model_id, event.usage_measurement_kind,
    event.input_tokens, event.cached_input_tokens,
    event.uncached_input_tokens, event.output_tokens,
    event.cache_creation_input_tokens,
    event.provider_usage_json, event.created_at
FROM model_usage_events event
WHERE event.binding_id = ? AND event.source_event_key = ?;

-- name: InsertModelUsageEvent :one
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, billing_provider_id,
    billing_provider_source, model_id, usage_measurement_kind,
    input_tokens, cached_input_tokens, uncached_input_tokens, output_tokens,
    cache_creation_input_tokens,
    provider_usage_json,
    input_cost_nanos, cached_input_cost_nanos, output_cost_nanos,
    estimated_cost_nanos, pricing_version,
    source_event_key, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: EnrichModelUsageEventCacheCreation :execrows
-- Replaying a durable prefix can supply the cache-write bucket for an event
-- stored before migration 0131 existed, exactly like the bounded provider
-- object above. A captured bucket is never overwritten: the column fills once,
-- then the write-once event contract holds.
UPDATE model_usage_events
SET cache_creation_input_tokens = sqlc.arg(cache_creation_input_tokens)
WHERE id = sqlc.arg(id)
  AND cache_creation_input_tokens IS NULL;

-- name: RehomeOpenUsageEventToReplacementSource :execrows
-- A physically replaced transcript re-emits the same logical event under the
-- same stable key, so the replay deduplicates against the row the retired
-- generation left behind and the row keeps pointing at that generation. Repair
-- skips a source retired as artifact_replaced, and the replacement owns no row
-- for the event, so an attribution that never landed can never land.
--
-- Only an open attribution moves, and open means the same thing here as
-- everywhere else: unattributed, or attributed by inference and therefore still
-- replaceable. Leaving an inferred row on the retired generation would strand a
-- guess exactly where no observation can reach it. A row attributed by
-- observation stays put: it was collected under the generation it names, and
-- that is a fact about how it was observed rather than a stale pointer.
UPDATE model_usage_events
SET usage_source_id = sqlc.arg(usage_source_id)
WHERE model_usage_events.id = sqlc.arg(id)
  AND model_usage_events.usage_source_id = sqlc.arg(expected_usage_source_id)
  AND (model_usage_events.billing_provider_id IS NULL
       OR model_usage_events.billing_provider_source = 'inferred')
  AND EXISTS (
      SELECT 1
      FROM usage_sources replacement
      WHERE replacement.id = sqlc.arg(usage_source_id)
        AND replacement.binding_id = model_usage_events.binding_id
  );

-- name: PromoteInferredUsageEventToObserved :execrows
-- A later observation supersedes an inferred billing provider and every cost
-- derived from that inference. ApplyUsageChunk rehomes replacement-generation
-- rows before this statement, so the source guard also prevents promotion on a
-- stale generation.
UPDATE model_usage_events
SET billing_provider_id = sqlc.arg(billing_provider_id),
    billing_provider_source = 'observed',
    input_cost_nanos = sqlc.narg(input_cost_nanos),
    cached_input_cost_nanos = sqlc.narg(cached_input_cost_nanos),
    output_cost_nanos = sqlc.narg(output_cost_nanos),
    estimated_cost_nanos = sqlc.narg(estimated_cost_nanos),
    pricing_version = sqlc.arg(pricing_version)
WHERE id = sqlc.arg(id)
  AND usage_source_id = sqlc.arg(expected_usage_source_id)
  AND billing_provider_id = sqlc.arg(expected_billing_provider_id)
  AND billing_provider_source = 'inferred';

-- name: HasOpenUsageAttribution :one
-- Whether this source still owns an event a repair pass could finish. Cheaper
-- than listing them, and it is asked once per applied chunk on a routed
-- binding.
SELECT CAST(EXISTS (
    SELECT 1
    FROM model_usage_events
    WHERE model_usage_events.usage_source_id = ?
      AND (model_usage_events.billing_provider_id IS NULL
           OR model_usage_events.billing_provider_source = 'inferred')
) AS INTEGER);

-- name: EnrichModelUsageEventProviderUsage :execrows
-- Replaying a durable prefix can supply the bounded provider object for an event
-- stored before the capture existed. A captured object is never overwritten.
UPDATE model_usage_events
SET provider_usage_json = sqlc.arg(provider_usage_json)
WHERE id = sqlc.arg(id)
  AND provider_usage_json IS NULL;

-- name: TouchUsageBinding :exec
UPDATE usage_bindings SET updated_at = ? WHERE id = ?;

-- name: UpsertModelUsageEventTiming :one
-- One timing row per model_usage_events.id (Decision 2 of the timing ADR).
-- Written atomically with its event inside ApplyUsageChunk: an INSERT for a
-- newly written event, and an upsert when a replayed/replacement generation
-- re-derives the same logical event (the parent row identity does not change
-- across a rehome, so the timing row is refreshed in place).
INSERT INTO model_usage_event_timing (
    event_id, round_seq, llm_ms, tool_ms, first_token_ms, created_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (event_id) DO UPDATE SET
    round_seq      = excluded.round_seq,
    llm_ms         = excluded.llm_ms,
    tool_ms        = excluded.tool_ms,
    first_token_ms = excluded.first_token_ms,
    created_at     = excluded.created_at
RETURNING event_id;

-- name: ListUsageCostCandidates :many
SELECT
    event.id,
    event.binding_id,
    event.provider_id,
    event.billing_provider_id,
    event.model_id,
    event.usage_measurement_kind,
    event.input_tokens,
    event.cached_input_tokens,
    event.uncached_input_tokens,
    event.output_tokens,
    event.provider_usage_json,
    event.pricing_version,
    event.source_event_key
FROM model_usage_events event
WHERE event.billing_provider_id IS NOT NULL
  AND CASE lower(trim(event.billing_provider_id))
        WHEN 'z.ai' THEN 'zai'
        ELSE lower(trim(event.billing_provider_id))
  END = sqlc.arg(billing_provider_id)
  AND event.estimated_cost_nanos IS NULL
  AND event.pricing_version <> sqlc.arg(pricing_version)
  AND event.id > sqlc.arg(after_id)
ORDER BY event.id
LIMIT 256;

-- name: UpdateUsageCostCandidate :one
UPDATE model_usage_events
SET input_cost_nanos = sqlc.narg(input_cost_nanos),
    cached_input_cost_nanos = sqlc.narg(cached_input_cost_nanos),
    output_cost_nanos = sqlc.narg(output_cost_nanos),
    estimated_cost_nanos = sqlc.narg(estimated_cost_nanos),
    pricing_version = sqlc.arg(attempted_pricing_version)
WHERE id = sqlc.arg(id)
  AND binding_id = sqlc.arg(binding_id)
  AND billing_provider_id = sqlc.arg(expected_billing_provider_id)
  AND model_id = sqlc.arg(expected_model_id)
  AND usage_measurement_kind = sqlc.arg(expected_usage_measurement_kind)
  AND input_tokens IS sqlc.narg(expected_input_tokens)
  AND cached_input_tokens IS sqlc.narg(expected_cached_input_tokens)
  AND uncached_input_tokens IS sqlc.narg(expected_uncached_input_tokens)
  AND output_tokens IS sqlc.narg(expected_output_tokens)
  AND provider_usage_json IS sqlc.narg(expected_provider_usage_json)
  AND source_event_key = sqlc.arg(expected_source_event_key)
  AND pricing_version = sqlc.arg(expected_pricing_version)
  AND estimated_cost_nanos IS NULL
RETURNING binding_id;

-- name: ListLegacyUsageSourceIDs :many
SELECT DISTINCT us.id
FROM usage_sources us
JOIN model_usage_events mue ON mue.usage_source_id = us.id
-- Open attribution: never attributed, or attributed only by inference and so
-- still replaceable by an observation.
WHERE mue.billing_provider_id IS NULL
   OR mue.billing_provider_source = 'inferred'
ORDER BY us.id;

-- name: ListLegacyUsageEvents :many
SELECT
    event.id,
    event.binding_id,
    event.usage_source_id,
    event.provider_id,
    event.billing_provider_id,
    event.billing_provider_source,
    event.model_id,
    event.usage_measurement_kind,
    event.input_tokens,
    event.cached_input_tokens,
    event.uncached_input_tokens,
    event.output_tokens,
    event.provider_usage_json,
    event.pricing_version,
    event.source_event_key
FROM model_usage_events event
WHERE event.usage_source_id = sqlc.arg(usage_source_id)
  AND (event.billing_provider_id IS NULL OR event.billing_provider_source = 'inferred')
ORDER BY event.id;

-- name: UpdateLegacyUsageEvent :one
UPDATE model_usage_events
SET billing_provider_id = sqlc.arg(billing_provider_id),
    billing_provider_source = sqlc.arg(billing_provider_source),
    -- The reparse is the only way a pre-capture event can ever gain its bounded
    -- provider object, and it is exactly what makes the event priceable.
    provider_usage_json = sqlc.narg(provider_usage_json),
    input_cost_nanos = sqlc.narg(input_cost_nanos),
    cached_input_cost_nanos = sqlc.narg(cached_input_cost_nanos),
    output_cost_nanos = sqlc.narg(output_cost_nanos),
    estimated_cost_nanos = sqlc.narg(estimated_cost_nanos),
    pricing_version = sqlc.arg(pricing_version)
WHERE model_usage_events.id = sqlc.arg(id)
  AND model_usage_events.binding_id = sqlc.arg(binding_id)
  AND model_usage_events.usage_source_id = sqlc.arg(usage_source_id)
  -- Writable while the attribution is still open. An observation is final, so
  -- this can promote an inference exactly once and never revise an observation.
  AND (model_usage_events.billing_provider_id IS NULL
       OR model_usage_events.billing_provider_source = 'inferred')
  AND model_usage_events.billing_provider_id IS sqlc.narg(expected_billing_provider_id)
  AND model_usage_events.billing_provider_source IS sqlc.narg(expected_billing_provider_source)
  AND model_usage_events.provider_id = sqlc.arg(expected_provider_id)
  AND model_usage_events.model_id = sqlc.arg(expected_model_id)
  AND model_usage_events.usage_measurement_kind = sqlc.arg(expected_usage_measurement_kind)
  AND model_usage_events.input_tokens IS sqlc.narg(expected_input_tokens)
  AND model_usage_events.cached_input_tokens IS sqlc.narg(expected_cached_input_tokens)
  AND model_usage_events.uncached_input_tokens IS sqlc.narg(expected_uncached_input_tokens)
  AND model_usage_events.output_tokens IS sqlc.narg(expected_output_tokens)
  AND model_usage_events.provider_usage_json IS sqlc.narg(expected_provider_usage_json)
  AND model_usage_events.source_event_key = sqlc.arg(expected_source_event_key)
  AND model_usage_events.pricing_version = sqlc.arg(expected_pricing_version)
  AND EXISTS (
      SELECT 1
      FROM usage_sources source
      WHERE source.id = model_usage_events.usage_source_id
        AND source.file_identity = sqlc.arg(expected_file_identity)
        AND source.byte_offset = sqlc.arg(expected_byte_offset)
        AND source.parser_state_json = sqlc.arg(expected_parser_state_json)
        AND source.updated_at = sqlc.arg(expected_source_updated_at)
        AND NOT (
            source.state = 'complete'
            AND source.last_error_code = 'artifact_replaced'
        )
  )
RETURNING binding_id;

-- name: AggregateUsageBySessionHarnessModel :many
SELECT
    ub.harness,
    mue.model_id,
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COALESCE(SUM(mue.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COUNT(mue.input_tokens) AS INTEGER) AS known_input_token_count,
    CAST(COALESCE(SUM(mue.cached_input_tokens), 0) AS INTEGER) AS cached_input_tokens,
    CAST(COUNT(mue.cached_input_tokens) AS INTEGER) AS known_cached_input_token_count,
    CAST(COALESCE(SUM(mue.uncached_input_tokens), 0) AS INTEGER) AS uncached_input_tokens,
    CAST(COUNT(mue.uncached_input_tokens) AS INTEGER) AS known_uncached_input_token_count,
    CAST(COALESCE(SUM(mue.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COUNT(mue.output_tokens) AS INTEGER) AS known_output_token_count,
    CAST(COALESCE(SUM(mue.cache_creation_input_tokens), 0) AS INTEGER) AS cache_creation_input_tokens,
    CAST(COUNT(mue.cache_creation_input_tokens) AS INTEGER) AS known_cache_creation_token_count,
    CAST(COUNT(mue.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(mue.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(mue.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(mue.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(mue.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(mue.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(mue.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(mue.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM model_usage_events mue
JOIN usage_bindings ub ON ub.id = mue.binding_id
WHERE ub.session_id = ?
-- Grouped by model alone. The billing provider is a pricing input, not a
-- product distinction: each event was already costed against its own provider's
-- rates, so summing across them is exact. Splitting on it only ever surfaced
-- AO's own attribution gaps as duplicate rows for one model.
GROUP BY ub.harness, mue.model_id
ORDER BY SUM(mue.input_tokens + mue.output_tokens) DESC, ub.harness, mue.model_id;

-- name: AggregateUsageSummary :one
-- Global (cross-session) usage summary over an optional created_at range. The
-- from/to bounds are inclusive; passing NULL for either omits that bound. The
-- optional source kind and model id filters are exact matches; passing NULL for
-- either omits that filter. Every counter mirrors the per-session aggregate: a
-- summed metric is only meaningful when every event in the scope carried it, so
-- the known_*_count columns let the service drop any component that is not
-- fully known.
SELECT
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COALESCE(SUM(mue.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COUNT(mue.input_tokens) AS INTEGER) AS known_input_token_count,
    CAST(COALESCE(SUM(mue.cached_input_tokens), 0) AS INTEGER) AS cached_input_tokens,
    CAST(COUNT(mue.cached_input_tokens) AS INTEGER) AS known_cached_input_token_count,
    CAST(COALESCE(SUM(mue.uncached_input_tokens), 0) AS INTEGER) AS uncached_input_tokens,
    CAST(COUNT(mue.uncached_input_tokens) AS INTEGER) AS known_uncached_input_token_count,
    CAST(COALESCE(SUM(mue.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COUNT(mue.output_tokens) AS INTEGER) AS known_output_token_count,
    CAST(COALESCE(SUM(mue.cache_creation_input_tokens), 0) AS INTEGER) AS cache_creation_input_tokens,
    CAST(COUNT(mue.cache_creation_input_tokens) AS INTEGER) AS known_cache_creation_token_count,
    CAST(COUNT(mue.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(mue.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(mue.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(mue.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(mue.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(mue.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(mue.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(mue.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM model_usage_events mue
WHERE (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
  AND (sqlc.narg(source) IS NULL OR EXISTS (
      SELECT 1
      FROM usage_sources us
      WHERE us.id = mue.usage_source_id
        AND us.kind = sqlc.narg(source)
  ))
  AND (sqlc.narg(model) IS NULL OR mue.model_id = sqlc.narg(model));

-- name: ListUsageSummaryDimensions :many
-- Distinct (usage source kind, model id) pairs present in the summary scope.
-- The scope is the same range and optional source/model filters the summary
-- endpoint applies, so dropdown options stay meaningful: choosing a source
-- narrows the model options to models that actually carry events from that
-- source. Events without a durable source row cannot be filtered by source and
-- so are excluded here.
SELECT DISTINCT
    us.kind AS source_kind,
    mue.model_id
FROM model_usage_events mue
JOIN usage_sources us ON us.id = mue.usage_source_id
WHERE (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
  AND (sqlc.narg(source) IS NULL OR us.kind = sqlc.narg(source))
  AND (sqlc.narg(model) IS NULL OR mue.model_id = sqlc.narg(model))
ORDER BY us.kind, mue.model_id;

-- name: AggregateUsageByModel :many
-- Global per-model usage rollup over an optional created_at range with optional
-- source/model filters (the same optional filter params the summary accepts).
-- Every counter mirrors the summary aggregate per model: a summed metric is only
-- meaningful when every event in the group carried it, so the known_*_count
-- columns let the service drop any component that is not fully known. The
-- billing provider is a pricing input rather than a grouping key: a model stays
-- one row even when more than one provider served it.
SELECT
    mue.model_id AS group_key,
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COALESCE(SUM(mue.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COUNT(mue.input_tokens) AS INTEGER) AS known_input_token_count,
    CAST(COALESCE(SUM(mue.cached_input_tokens), 0) AS INTEGER) AS cached_input_tokens,
    CAST(COUNT(mue.cached_input_tokens) AS INTEGER) AS known_cached_input_token_count,
    CAST(COALESCE(SUM(mue.uncached_input_tokens), 0) AS INTEGER) AS uncached_input_tokens,
    CAST(COUNT(mue.uncached_input_tokens) AS INTEGER) AS known_uncached_input_token_count,
    CAST(COALESCE(SUM(mue.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COUNT(mue.output_tokens) AS INTEGER) AS known_output_token_count,
    CAST(COALESCE(SUM(mue.cache_creation_input_tokens), 0) AS INTEGER) AS cache_creation_input_tokens,
    CAST(COUNT(mue.cache_creation_input_tokens) AS INTEGER) AS known_cache_creation_token_count,
    CAST(COUNT(mue.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(mue.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' THEN 1 END) AS INTEGER) AS observed_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' THEN 1 END) AS INTEGER) AS inferred_event_count,
    CAST(COUNT(mue.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(mue.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(mue.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(mue.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(mue.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(mue.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM model_usage_events mue
LEFT JOIN usage_sources us ON us.id = mue.usage_source_id
WHERE (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
  AND (sqlc.narg(source) IS NULL OR us.kind = sqlc.narg(source))
  AND (sqlc.narg(model) IS NULL OR mue.model_id = sqlc.narg(model))
GROUP BY mue.model_id
ORDER BY mue.model_id;

-- name: AggregateUsageByProvider :many
-- Global per-billing-provider usage rollup over an optional created_at range
-- with optional source/model filters (the same optional filter params the
-- summary accepts). Events without a billing provider attribution
-- (billing_provider_id IS NULL) group into the empty-string bucket so request
-- counts and token totals never silently vanish from the provider view.
SELECT
    COALESCE(mue.billing_provider_id, '') AS group_key,
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COALESCE(SUM(mue.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COUNT(mue.input_tokens) AS INTEGER) AS known_input_token_count,
    CAST(COALESCE(SUM(mue.cached_input_tokens), 0) AS INTEGER) AS cached_input_tokens,
    CAST(COUNT(mue.cached_input_tokens) AS INTEGER) AS known_cached_input_token_count,
    CAST(COALESCE(SUM(mue.uncached_input_tokens), 0) AS INTEGER) AS uncached_input_tokens,
    CAST(COUNT(mue.uncached_input_tokens) AS INTEGER) AS known_uncached_input_token_count,
    CAST(COALESCE(SUM(mue.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COUNT(mue.output_tokens) AS INTEGER) AS known_output_token_count,
    CAST(COALESCE(SUM(mue.cache_creation_input_tokens), 0) AS INTEGER) AS cache_creation_input_tokens,
    CAST(COUNT(mue.cache_creation_input_tokens) AS INTEGER) AS known_cache_creation_token_count,
    CAST(COUNT(mue.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(mue.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' THEN 1 END) AS INTEGER) AS observed_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' THEN 1 END) AS INTEGER) AS inferred_event_count,
    CAST(COUNT(mue.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(mue.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(mue.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(mue.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(mue.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(mue.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM model_usage_events mue
LEFT JOIN usage_sources us ON us.id = mue.usage_source_id
WHERE (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
  AND (sqlc.narg(source) IS NULL OR us.kind = sqlc.narg(source))
  AND (sqlc.narg(model) IS NULL OR mue.model_id = sqlc.narg(model))
GROUP BY COALESCE(mue.billing_provider_id, '')
ORDER BY COALESCE(mue.billing_provider_id, '');

-- name: ListUsageRequestLog :many
-- Newest-first, bounded page of normalized usage events over an optional
-- created_at range and optional exact source kind / model id filters, plus a
-- keyset cursor. The caller requests limit+1 rows to detect whether another
-- page exists, then truncates to limit. Ordering is by event id descending,
-- which is monotonic with insertion, NULL-safe (created_at is nullable and can
-- be backfilled out of timestamp order), and stable: the before cursor filters
-- by id, so a page never shifts as newer events are appended and no row can
-- vanish or repeat between pages.
--
-- The timing join is nil-preserving (Decision 2/3 of the timing ADR): events
-- without a timing row (pre-deployment history, uncertified boundaries) keep
-- NULL llm_ms/first_token_ms, which the caller renders as the unknown marker,
-- never zero. The join is 1:1 (timing.event_id is the primary key), so it
-- cannot multiply rows or disturb the id keyset paging.
SELECT
    event.id,
    event.created_at,
    event.billing_provider_id,
    event.model_id,
    event.input_tokens,
    event.cached_input_tokens,
    event.output_tokens,
    event.cache_creation_input_tokens,
    event.estimated_cost_nanos,
    source.kind AS source_kind,
    binding.session_id,
    timing.llm_ms AS llm_ms,
    timing.first_token_ms AS first_token_ms,
    CAST(CASE WHEN s.id IS NULL THEN 0 ELSE 1 END AS INTEGER) AS session_exists
FROM model_usage_events event
JOIN usage_sources source ON source.id = event.usage_source_id
JOIN usage_bindings binding ON binding.id = event.binding_id
LEFT JOIN sessions s ON s.id = binding.session_id
LEFT JOIN model_usage_event_timing timing ON timing.event_id = event.id
WHERE (sqlc.narg(from) IS NULL OR event.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR event.created_at <= sqlc.narg(to))
  AND (sqlc.narg(source) IS NULL OR source.kind = sqlc.narg(source))
  AND (sqlc.narg(model) IS NULL OR event.model_id = sqlc.narg(model))
  AND (sqlc.narg(before_id) IS NULL OR event.id < sqlc.narg(before_id))
ORDER BY event.id DESC
LIMIT sqlc.arg(limit);

-- name: AggregateUsageTrend :many
-- Cross-session usage bucketed by UTC-aligned created_at intervals (hour or
-- day). Every counter mirrors the global summary aggregate: a summed metric is
-- only meaningful when every event in the bucket carried it, so the
-- known_*_count columns let the service drop any component that is not fully
-- known. The optional source filter matches the usage source kind that
-- produced the event; the optional model filter matches the model id exactly.
-- Events without a created_at cannot be placed in a bucket and are excluded.
-- Bucket keys are unix epoch divided by the bucket width in seconds (3600 for
-- hour, 86400 for day): the driver stores TIMESTAMP as a UTC text the SQLite
-- date functions cannot parse, so the key is derived from its fixed prefix
-- instead of strftime.
SELECT
    bucket_key,
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COALESCE(SUM(rows.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COUNT(rows.input_tokens) AS INTEGER) AS known_input_token_count,
    CAST(COALESCE(SUM(rows.cached_input_tokens), 0) AS INTEGER) AS cached_input_tokens,
    CAST(COUNT(rows.cached_input_tokens) AS INTEGER) AS known_cached_input_token_count,
    CAST(COALESCE(SUM(rows.uncached_input_tokens), 0) AS INTEGER) AS uncached_input_tokens,
    CAST(COUNT(rows.uncached_input_tokens) AS INTEGER) AS known_uncached_input_token_count,
    CAST(COALESCE(SUM(rows.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COUNT(rows.output_tokens) AS INTEGER) AS known_output_token_count,
    CAST(COALESCE(SUM(rows.cache_creation_input_tokens), 0) AS INTEGER) AS cache_creation_input_tokens,
    CAST(COUNT(rows.cache_creation_input_tokens) AS INTEGER) AS known_cache_creation_token_count,
    CAST(COUNT(rows.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(rows.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN rows.billing_provider_source = 'observed' AND (
        rows.estimated_cost_nanos IS NOT NULL OR rows.input_cost_nanos IS NOT NULL OR
        rows.cached_input_cost_nanos IS NOT NULL OR rows.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN rows.billing_provider_source = 'inferred' AND (
        rows.estimated_cost_nanos IS NOT NULL OR rows.input_cost_nanos IS NOT NULL OR
        rows.cached_input_cost_nanos IS NOT NULL OR rows.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(rows.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(rows.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN rows.estimated_cost_nanos IS NULL THEN rows.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(rows.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(rows.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN rows.estimated_cost_nanos IS NULL THEN rows.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(rows.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(rows.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN rows.estimated_cost_nanos IS NULL THEN rows.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM (
    SELECT
        unixepoch(substr(mue.created_at, 1, 19)) / CAST(sqlc.arg(bucket_seconds) AS INTEGER) AS bucket_key,
        mue.input_tokens,
        mue.cached_input_tokens,
        mue.uncached_input_tokens,
        mue.output_tokens,
        mue.cache_creation_input_tokens,
        mue.estimated_cost_nanos,
        mue.billing_provider_source,
        mue.input_cost_nanos,
        mue.cached_input_cost_nanos,
        mue.output_cost_nanos
    FROM model_usage_events mue
    JOIN usage_sources us ON us.id = mue.usage_source_id
    WHERE mue.created_at IS NOT NULL
      AND (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
      AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
      AND (sqlc.narg(source) IS NULL OR us.kind = sqlc.narg(source))
      AND (sqlc.narg(model) IS NULL OR mue.model_id = sqlc.narg(model))
) rows
GROUP BY 1
ORDER BY 1;

-- name: GetUsageSessionIncomplete :one
SELECT CAST(COALESCE((
    SELECT incomplete FROM usage_session_integrity WHERE session_id = ?
), 0) AS INTEGER);

-- name: ListCompactSessionUsage :many
SELECT
    ub.session_id,
    CAST(COALESCE(SUM(mue.input_tokens) + SUM(mue.output_tokens), 0) AS INTEGER) AS processed_tokens,
    CAST(COUNT(mue.input_tokens) = COUNT(*) AND COUNT(mue.output_tokens) = COUNT(*) AS INTEGER) AS processed_tokens_known,
    CAST(COALESCE(integrity.incomplete, 0) AS INTEGER) AS incomplete,
    CAST(COUNT(*) AS INTEGER) AS event_count,
    CAST(COUNT(mue.estimated_cost_nanos) AS INTEGER) AS priced_event_count,
    CAST(COALESCE(SUM(mue.estimated_cost_nanos), 0) AS INTEGER) AS priced_total_nanos,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'observed' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS observed_cost_event_count,
    CAST(COUNT(CASE WHEN mue.billing_provider_source = 'inferred' AND (
        mue.estimated_cost_nanos IS NOT NULL OR mue.input_cost_nanos IS NOT NULL OR
        mue.cached_input_cost_nanos IS NOT NULL OR mue.output_cost_nanos IS NOT NULL
    ) THEN 1 END) AS INTEGER) AS inferred_cost_event_count,
    CAST(COUNT(mue.input_cost_nanos) AS INTEGER) AS known_input_count,
    CAST(COALESCE(SUM(mue.input_cost_nanos), 0) AS INTEGER) AS known_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_input_nanos,
    CAST(COUNT(mue.cached_input_cost_nanos) AS INTEGER) AS known_cached_input_count,
    CAST(COALESCE(SUM(mue.cached_input_cost_nanos), 0) AS INTEGER) AS known_cached_input_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.cached_input_cost_nanos END), 0) AS INTEGER) AS unpriced_known_cached_input_nanos,
    CAST(COUNT(mue.output_cost_nanos) AS INTEGER) AS known_output_count,
    CAST(COALESCE(SUM(mue.output_cost_nanos), 0) AS INTEGER) AS known_output_nanos,
    CAST(COALESCE(SUM(CASE WHEN mue.estimated_cost_nanos IS NULL THEN mue.output_cost_nanos END), 0) AS INTEGER) AS unpriced_known_output_nanos
FROM model_usage_events mue
JOIN usage_bindings ub ON ub.id = mue.binding_id
JOIN sessions s ON s.id = ub.session_id
LEFT JOIN usage_session_integrity integrity ON integrity.session_id = ub.session_id
WHERE (sqlc.arg(project_id) = '' OR s.project_id = sqlc.arg(project_id))
GROUP BY ub.session_id, s.project_id, s.num, integrity.incomplete
ORDER BY s.project_id, s.num;

-- name: ListModelUsageEventTiming :many
-- Request-log read model: every usage event in the range with its timing row
-- LEFT JOINed. Events without a timing row (pre-timing ingestions, or a source
-- whose timing facts were never certified) come back with NULL durations and a
-- zero round_seq; the caller renders the unknown marker, never a zero.
SELECT
    mue.id AS event_id,
    mue.binding_id,
    mue.created_at,
    mue.source_event_key,
    timing.round_seq,
    timing.llm_ms,
    timing.tool_ms,
    timing.first_token_ms
FROM model_usage_events mue
LEFT JOIN model_usage_event_timing timing ON timing.event_id = mue.id
WHERE (sqlc.narg(from) IS NULL OR mue.created_at >= sqlc.narg(from))
  AND (sqlc.narg(to) IS NULL OR mue.created_at <= sqlc.narg(to))
ORDER BY mue.id DESC;

-- name: AggregateSessionRuntimeTiming :one
-- Native/TUI-mode runtime statistics for one session (timing ADR #9): one
-- rollup over certified usage events with their timing rows LEFT JOINed.
-- StepCount counts every event (subagent sources included). RoundCount counts
-- distinct (root source, round) pairs: only root-generation sources
-- (subagent_id = '') contribute rounds, so a session's rounds count user
-- exchanges, never agent-internal spawns. SUM() skips NULL durations, so a
-- session whose timing facts were never certified (pre-deployment ingestions,
-- a non-certified harness) returns NULL totals and the caller renders the
-- unknown marker, never a zero. RateOutputTokens/RateLLMMS are the
-- ratio-of-sums numerator and denominator for the session average output tok/s
-- (ADR Decision 3): only events carrying both a positive LLM elapsed and known
-- output tokens.
SELECT
    CAST(COUNT(mue.id) AS INTEGER) AS step_count,
    CAST(COUNT(timing.event_id) AS INTEGER) AS timing_row_count,
    CAST(COUNT(DISTINCT CASE WHEN src.subagent_id = '' THEN src.id || ':' || timing.round_seq END) AS INTEGER) AS round_count,
    CAST(COALESCE(SUM(timing.llm_ms), -1) AS INTEGER) AS llm_ms_total,
    CAST(COALESCE(SUM(timing.tool_ms), -1) AS INTEGER) AS tool_ms_total,
    CAST(COALESCE(SUM(timing.first_token_ms), -1) AS INTEGER) AS first_token_ms_sum,
    CAST(COUNT(timing.first_token_ms) AS INTEGER) AS first_token_known,
    CAST(COALESCE(SUM(CASE WHEN timing.llm_ms IS NOT NULL AND mue.output_tokens IS NOT NULL AND timing.llm_ms > 0 THEN mue.output_tokens END), -1) AS INTEGER) AS rate_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN timing.llm_ms IS NOT NULL AND mue.output_tokens IS NOT NULL AND timing.llm_ms > 0 THEN timing.llm_ms END), -1) AS INTEGER) AS rate_llm_ms
FROM model_usage_events mue
JOIN usage_bindings ub ON ub.id = mue.binding_id
JOIN usage_sources src ON src.id = mue.usage_source_id
LEFT JOIN model_usage_event_timing timing ON timing.event_id = mue.id
WHERE ub.session_id = ?;

-- name: ListConversationRuntimeTurnFacts :many
-- Chat-mode runtime statistics (timing ADR #9): one row per conversation turn
-- on the session's active branch lineage. Turns are restricted to the session's
-- own conversation and to the active lineage the timeline shows: like
-- SelectConversationTurns, a turn only counts when it carries in-lineage
-- content at or before its branch's fork cutoff, so an edit-fork ancestor turn
-- whose items all fall beyond the cutoff (its replacement lives on the child
-- branch) is dropped instead of double-counting chat LLM time. Rolled-back,
-- promoted, and cancelled turns are discarded; daemon-only turns (compaction,
-- provider-adopted resumes) carry no prompt. The store pairs these rows with
-- ListConversationRuntimeContentRows to derive the per-turn facts.
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.session_id = sqlc.arg(session_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT
    turn.id AS turn_id,
    turn.state AS state,
    turn.requested_at AS requested_at,
    turn.started_at AS started_at,
    turn.completed_at AS completed_at
FROM conversation_turns turn
JOIN active_path AS path ON path.branch_id = turn.branch_id
WHERE turn.conversation_id IN (SELECT conversations.id FROM conversations WHERE conversations.session_id = sqlc.arg(session_id))
  AND turn.promoted_to_turn_id IS NULL
  AND turn.rolled_back_at IS NULL
  AND turn.state <> 'cancelled'
  AND (path.max_sequence IS NULL OR EXISTS (
      SELECT 1 FROM conversation_messages AS lineage_message
      WHERE lineage_message.turn_id = turn.id
        AND lineage_message.sequence <= path.max_sequence
      UNION ALL
      SELECT 1 FROM conversation_activities AS lineage_activity
      WHERE lineage_activity.turn_id = turn.id
        AND lineage_activity.sequence <= path.max_sequence
  ))
ORDER BY turn.requested_at, turn.rowid;

-- name: ListConversationRuntimeContentRows :many
-- Chat-mode runtime statistics content rows (timing ADR #9): every timeline
-- item of the session's conversation on the active lineage, unified across
-- messages and activities so the store can derive per-turn first-content,
-- tool elapsed, and step counts with full timestamp precision (the driver's
-- stored text format is not parseable by SQLite date functions). Rows with a
-- NULL turn_id are dropped by the caller: only turn-attributed work counts.
-- Messages also carry revision and streaming so the caller can tell a row
-- inserted whole by the settle fallback (revision 0, not streaming) from one
-- that entered the streaming pipeline; activities always report 0 because
-- their created_at certifies first content unconditionally.
WITH RECURSIVE active_path(branch_id, max_sequence) AS (
    SELECT conversations.active_branch_id, CAST(NULL AS INTEGER)
    FROM conversations
    WHERE conversations.session_id = sqlc.arg(session_id)
    UNION ALL
    SELECT branch.parent_branch_id,
           CASE
               WHEN path.max_sequence IS NULL THEN branch.fork_after_sequence
               WHEN branch.fork_after_sequence < path.max_sequence THEN branch.fork_after_sequence
               ELSE path.max_sequence
           END
    FROM active_path AS path
    JOIN conversation_branches AS branch ON branch.id = path.branch_id
    WHERE branch.parent_branch_id IS NOT NULL
)
SELECT
    'message' AS row_kind,
    conversation_messages.turn_id AS turn_id,
    CAST(conversation_messages.role AS TEXT) AS role,
    CAST(conversation_messages.origin AS TEXT) AS origin,
    '' AS status,
    conversation_messages.revision AS revision,
    conversation_messages.streaming AS streaming,
    conversation_messages.created_at AS created_at,
    conversation_messages.updated_at AS updated_at
FROM conversation_messages
JOIN active_path AS path ON path.branch_id = conversation_messages.branch_id
WHERE conversation_messages.conversation_id IN (SELECT conversations.id FROM conversations WHERE conversations.session_id = sqlc.arg(session_id))
  AND (path.max_sequence IS NULL OR conversation_messages.sequence <= path.max_sequence)
UNION ALL
SELECT
    'activity' AS row_kind,
    conversation_activities.turn_id AS turn_id,
    '' AS role,
    '' AS origin,
    CAST(conversation_activities.status AS TEXT) AS status,
    0 AS revision,
    0 AS streaming,
    conversation_activities.created_at AS created_at,
    conversation_activities.updated_at AS updated_at
FROM conversation_activities
JOIN active_path AS path ON path.branch_id = conversation_activities.branch_id
WHERE conversation_activities.conversation_id IN (SELECT conversations.id FROM conversations WHERE conversations.session_id = sqlc.arg(session_id))
  AND (path.max_sequence IS NULL OR conversation_activities.sequence <= path.max_sequence)
ORDER BY created_at;

-- name: ListACPUsageEventConversations :many
-- Conversations whose durable provider-event archive carries usage facts, with
-- the newest usage row id. The ACP certifier compares last_event_id against
-- each conversation's acp_usage source cursor to find work; the scan is the
-- backfill entry point as well as the live poll, because the archive is
-- durable AO state rather than a rotated transcript.
SELECT
    cpe.conversation_id,
    cpe.session_id,
    CAST(MAX(cpe.id) AS INTEGER) AS last_event_id
FROM conversation_provider_events cpe
WHERE cpe.method = 'usage'
GROUP BY cpe.conversation_id, cpe.session_id
ORDER BY cpe.conversation_id;

-- name: ListACPUsageEventsAfter :many
-- One bounded, id-ordered page of the conversation's archived usage events
-- past the certifier cursor. The row id is the replay identity: it keys the
-- emitted model_usage_events.source_event_key, so a re-scan deduplicates
-- through the events table's UNIQUE(binding_id, source_event_key).
SELECT
    id,
    session_id,
    payload_json,
    received_at
FROM conversation_provider_events
WHERE conversation_id = sqlc.arg(conversation_id)
  AND method = 'usage'
  AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(limit);

-- name: SelectConversationUsageModel :one
-- The conversation's durable model choice (conversations.model; NULL when the
-- user never picked one and the provider default answered). The ACP usage
-- payload names no model, so this row is the attribution evidence.
SELECT model FROM conversations WHERE id = ?;

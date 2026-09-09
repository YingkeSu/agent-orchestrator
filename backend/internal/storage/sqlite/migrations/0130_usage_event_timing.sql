-- Summary: one request-level timing row per native certified usage event.
--
-- Decision 2 of docs/adr/0005-request-level-timing-metrics.md. Timing facts that
-- can only be recovered from the transcript (the transcript-clock intervals
-- between native records) are captured at ingestion and stored here, keyed 1:1
-- by model_usage_events.id. A NULL duration is an uncollected metric (the
-- bounding record was missing or predated capture); a stored non-negative
-- value is a known duration. The timing row is written atomically with its
-- event inside ApplyUsageChunk, so no event column changes and the certified
-- write-once event contract is untouched.
--
-- Retention cascades: event_id is an ON DELETE CASCADE foreign key, so a
-- deleted usage event (and by extension a deleted session) removes its timing
-- row with it. No CDC trigger is added; the existing usage_bindings trigger
-- invalidates the UI once per chunk, and the child row inherits that signal.
-- Backfill is explicitly out of scope (Decision 4): the table starts empty and
-- fills forward from events ingested after this migration lands.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE model_usage_event_timing (
    event_id        INTEGER PRIMARY KEY
                    REFERENCES model_usage_events (id) ON DELETE CASCADE,
    round_seq       INTEGER NOT NULL CHECK (round_seq >= 1),
    llm_ms          INTEGER CHECK (llm_ms IS NULL OR llm_ms >= 0),
    tool_ms         INTEGER CHECK (tool_ms IS NULL OR tool_ms >= 0),
    first_token_ms  INTEGER CHECK (first_token_ms IS NULL OR first_token_ms >= 0),
    created_at      TIMESTAMP NOT NULL
);

CREATE INDEX idx_model_usage_event_timing_round
    ON model_usage_event_timing (round_seq);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS model_usage_event_timing;
-- +goose StatementEnd

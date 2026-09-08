-- Summary: index model_usage_events.created_at for the global usage summary
-- time-range aggregation (GET /api/v1/usage/summary). Usage events are appended
-- in roughly ingestion order, but the summary filters by created_at over an
-- optional from/to range across every session, so a dedicated index keeps the
-- range scan from degenerating into a full table walk.
-- +goose Up
-- +goose StatementBegin
CREATE INDEX idx_model_usage_events_created_at ON model_usage_events (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_model_usage_events_created_at;
-- +goose StatementEnd

-- Summary: one nullable cache-creation (cache write) token counter per usage event.
--
-- Decision 1 of docs/adr/0006-cache-creation-split.md. The write bucket was
-- previously folded into uncached_input_tokens by the neutral normalizers and
-- survived only inside the bounded provider usage object. It becomes a fifth
-- provider-neutral counter on the event row, added here as a strict superset
-- of the 0115 shape: the folded totals, the table-level
-- input = cached + uncached CHECK, and every existing column are untouched.
--
-- NULL is an uncollected metric: events ingested before this migration, or an
-- event whose provider usage object was absent or oversized. A stored zero is
-- a known zero the certified record actually reported. Backfill is explicitly
-- out of scope (Decision 3): the column fills forward from events ingested
-- after this migration lands.
--
-- The standalone non-negative CHECK and the additive cross-column invariant
-- (cache_creation_input_tokens <= uncached_input_tokens, ADR sketch open
-- question 1) are both pinned in this column definition. SQLite accepts and
-- enforces a CHECK that reads another column under ADD COLUMN: every existing
-- row's value is NULL, which passes, and later writes are checked against the
-- current values of uncached_input_tokens (verified against the bundled
-- modernc.org/sqlite build during the implementing slice). The normalizers
-- already guarantee the invariant for everything ingested, so this pin is
-- defense in depth, not correctness.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE model_usage_events
    ADD COLUMN cache_creation_input_tokens INTEGER
    CHECK (cache_creation_input_tokens IS NULL
           OR (cache_creation_input_tokens >= 0
               AND (uncached_input_tokens IS NULL
                    OR cache_creation_input_tokens <= uncached_input_tokens)));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- DROP COLUMN is supported by the bundled driver (SQLite 3.35+; verified
-- against modernc.org/sqlite during the implementing slice). The column is
-- dropped together with its own column-level CHECK; no other CHECK, index, or
-- trigger references it.
ALTER TABLE model_usage_events DROP COLUMN cache_creation_input_tokens;
-- +goose StatementEnd

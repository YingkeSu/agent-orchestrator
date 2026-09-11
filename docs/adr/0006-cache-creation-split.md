# 6. Splitting cache-creation tokens from uncached input

Date: 2026-09-10
Status: Proposed

## Context

The V1 usage pipeline folds Anthropic cache-creation (cache write) tokens into
`uncached_input_tokens`. The fold was approved in PR #12 and is documented on
`UsageTokenMetrics`: "Cache writes are part of uncached input here; their
provider-specific split stays in the bounded provider usage object." Every
downstream surface inherits the fold — the global usage summary, the trend
chart, the per-model/per-provider tables, and the request log cannot show cache
creation as its own component. PR #19's review noted the provider-usage split
therefore needs to be surfaced at the API boundary, and issue #24 asks this ADR
to decide six questions so the implementation slice can proceed. This ADR is
the human architecture-review gate for that slice.

Today the fold happens in the two neutral normalizers:

- **Anthropic vocabulary** (`claude_main`, `claude_subagent`, and the Kimi
  wire, which AO normalizes into the same vocabulary):
  `normalizeAnthropicUsage` computes `uncached_input = direct_input +
  cache_creation_input`, so the write bucket disappears into the folded total.
  The raw counters survive only verbatim inside `provider_usage_json`.
- **OpenAI vocabulary** (`codex_rollout`): `normalizeOpenAIUsage` computes
  `uncached_input = input - cached_input`, with `cache_write_input_tokens`
  part of the uncached remainder for the same reason.

The estimator already depends on the split at price time: `cacheWriteSplitFor`
reads the write buckets back out of `provider_usage_json` (Anthropic's
`cache_creation_input_tokens` with its five-minute/one-hour tiers; Codex's
`last_token_usage.cache_write_input_tokens` with the parser's derived
`ao_derived_cache_write_input_tokens` fallback) so a model that publishes a
cache-write rate can be priced exactly. The fact is durable but not
first-class: it is a provider-shaped JSON blob per event, not a queryable
counter.

The repository rules this decision must respect (the same ones ADR 0005
states, and which the issue requires this ADR to stay consistent with):

- **Durable facts, derived display.** Aggregates are computed at read time in
  the service layer. Only facts that cannot be reconstructed later are
  persisted.
- **Unknown is nil, never zero.** `model_usage_events` nullable counters carry
  this today: a NULL column is an uncollected metric, a stored zero is a known
  zero.
- **Certified sources only.** Counters are captured only where a certified
  source's native usage record reports them; nothing is guessed.
- **Append migrations only.** Merged migrations are never edited; schema
  changes arrive as a new numbered migration.

### Correction to the RFC premise

Issue #24 describes cache creation as an Anthropic-only concept and expects
Codex rollout and Kimi wire to have "no such concept". That is not true of this
codebase, and the ADR must decide against the code as it is:

- **Kimi wire reports cache creation natively.** `kimiNativeUsage` decodes
  `inputCacheCreation` from every `usage.record` and feeds it through the same
  Anthropic-vocabulary normalizer (`parser_kimi.go`). A Kimi event's write
  bucket is exactly as certified as its other counters.
- **Codex rollouts report cache writes natively.** The `codexTokenVector`
  carries `cache_write_input_tokens`; the per-event value comes from the same
  baseline-delta arithmetic that certifies every other Codex counter
  (`cacheWrite := total.CacheWriteInputTokens - state.Baseline.CacheWriteInputTokens`),
  and when the rollout omits `last_token_usage` the parser persists that
  derived bucket under `ao_derived_cache_write_input_tokens` — "derived rather
  than invented". Pricing already charges gpt-5.6-class models from exactly
  this bucket.

Consequence: the per-source ingestion decision below captures the split from
all sources that report it, not from Anthropic transcripts alone. Treating
Kimi and Codex as permanently unknown would ship a statistics page where the
new card is silently empty for two of the four certified sources, and would
throw away counters the pipeline already trusts for pricing. A reviewer who
prefers the narrower RFC-literal scope can reject Decision 2 here — that is
what this gate is for.

## Definitions used throughout

- **Cache-creation (cache-write) tokens.** The provider-neutral name for tokens
  the provider charged as writing to its prompt cache. Per-provider synonyms:
  Anthropic `cache_creation_input_tokens` (with an optional ephemeral
  five-minute/one-hour split), OpenAI/Codex `cache_write_input_tokens`, Kimi
  `inputCacheCreation`. This ADR uses "cache creation" for the product surface
  (matching issue #24) and "write bucket" for the per-event counter.
- **Folded total.** `uncached_input_tokens`: every non-cache-read input token,
  writes included. It stays exactly as it is.
- **Neutral vector.** `UsageTokenMetrics`: input, cached input, uncached input,
  output. The write bucket becomes a fifth, provider-neutral counter beside
  them, not a member of the vector equation (see Decision 4).
- **Coverage rule.** The existing aggregation semantics: an aggregate component
  is nil unless every event in the scope reports that component; a scope with
  one unknown event has an unknown component, never a partial sum.
- **Known zero.** A counter the certified record actually reported as 0 — for
  Anthropic and Kimi records the creation counter is part of every usage
  object, so 0 is the normal no-writes reading.

## Decision 1: storage — one nullable column on `model_usage_events`; folded totals untouched

### A new append migration adds `cache_creation_input_tokens` to the event row

The write bucket is a token fact of the event itself, in the same category as
`uncached_input_tokens`: provider-reported (or derived by the same certified
arithmetic), write-once, and part of what makes one event this event. It is
stored as one more nullable counter on `model_usage_events`, added by a new
append migration (`0138_usage_cache_creation_split.sql`, sketch in the Storage
sketch summary below). The folded totals and the existing table-level CHECK
(`input_tokens = cached_input_tokens + uncached_input_tokens`) are untouched;
the 0115 rebuild's shape is preserved and the change is a strict superset.

This deliberately reverses the instinct ADR 0005 applied to timing, for the
opposite reason. ADR 0005 rejected timing columns on `model_usage_events`
because timing is a transcript-clock measurement added alongside the certified
facts — it would either pollute the replay comparison or silently diverge from
it. Cache creation is not a measurement layered on top of the event; it is one
of the counters the event certifies. It therefore *should* join the replay
comparison: `usageEventsEqual` marshals the whole event, so a parser change
that alters the write bucket is exactly as much a replay conflict as one that
alters uncached input. That is the desired semantics, not a hazard.

### Alternatives considered

1. **Child table, mirroring `model_usage_event_timing`.** Rejected. Timing
   earned a child table because it is a foreign family of transcript-clock
   measurements. A token counter in a child table would repeat the mistake
   migration 0115 retired: 0102's per-provider detail tables
   (`anthropic_usage_event_details` carried
   `anthropic_cache_creation_input_tokens`) were dropped precisely because a
   typed side table for provider counters cost more than it bought. A child
   table also forces a JOIN into every aggregate and cannot participate in the
   event's uniqueness/CAS replay contract.
2. **Derive at read time from `provider_usage_json`.** Rejected as the primary
   design. The bounded provider object is a fidelity archive, not a query
   surface: three provider shapes with different keys (and Codex's write bucket
   present only conditionally — nested in `last_token_usage` when the CLI emits
   it, else under the `ao_derived_` key), a JSON parse per event on every
   summary/trend/request-log read, and read models coupled to provider JSON
   shapes. The estimator can tolerate that at price time (one pass per
   unpriced event); page reads cannot.
3. **Persist an aggregate cache-creation table.** Rejected: violates the
   durable-facts rule; the component is derivable from event rows like every
   other aggregate.

### Consequences

- Every aggregate that sums the other counters gains the component with one
  more `SUM`/`COUNT` pair in the existing queries — no new read path.
- The certified write-once event contract, its CAS replay, and cost-candidate
  scans keep working unchanged; the new column joins the dedupe comparison
  automatically.
- Pre-capture rows read NULL and render unknown (Decision 3); no existing
  number changes value.
- No new index: no query filters or groups by the column; aggregates already
  scan by time/binding.

## Decision 2: ingestion — capture the split from every certified source that reports it

### Per-source capture, with honest nil only where the counter is genuinely absent

The implementing slice makes each certified parser emit the write bucket it
already has in hand into the new column:

| Source | Native counter | Availability |
| --- | --- | --- |
| `claude_main`, `claude_subagent` | `cache_creation_input_tokens` (+ optional `cache_creation.ephemeral_5m/1h`) | Present in every Anthropic usage object; a reported 0 is a known zero. The tier split keeps its existing `validAnthropicCacheCreation` validation and stays in `provider_usage_json` for pricing. |
| `kimi_wire` | `inputCacheCreation` | Present in every `usage.record`; normalized through the same Anthropic-vocabulary path as today. |
| `codex_rollout` | `cache_write_input_tokens` | Per-event value from the same baseline-delta arithmetic as the other Codex counters; `normalizeOpenAIUsage` already enforces `cacheWrite <= input - cached`. When the rollout reports only cumulative totals the bucket is the parser's derived value (the same one persisted as `ao_derived_cache_write_input_tokens` today) — certified by the same rule, not invented. |

Nil, never zero, applies to the genuinely unknown cases only:

- events ingested before the migration lands (Decision 3);
- an event whose provider usage object was absent or exceeded the storage
  bound, when that also leaves the write bucket unrecoverable;
- any future source whose usage record has no write concept at all — its rows
  carry NULL and the UI renders unknown.

A known zero stays zero: an Anthropic record that reported
`cache_creation_input_tokens: 0` produced an uncached total equal to its direct
input yesterday; it now additionally states the write bucket was zero. That is
more information, not less, and it is exactly what the nil-never-zero rule
protects.

### Alternatives considered

1. **Anthropic-only capture (the RFC's literal enumeration).** Rejected: see
   the premise correction. It would leave Kimi and Codex — sources that do
   report the bucket, and whose other counters are certified by identical
   rules — permanently unknown, and it contradicts the capture principle the
   issue itself states ("capture per source where the provider reports it").
2. **Decode a missing counter as 0.** Rejected: collapses unknown into zero
   and corrupts both the component and any future coverage math. Absent stays
   NULL.
3. **Capture only the total, deferring the five-minute/one-hour tier split to
   a column.** Deferred: the tiers are already durably captured verbatim in
   `provider_usage_json` and are a pricing concern, not a display one; this
   ADR's surface is the total write bucket. If a later slice wants tier
   columns, that is a new decision (and likely an extension of
   `cacheWriteSplitFor`, not a display change).

### Consequences

- The parser changes are emission-only: every value the slice persists is
  already computed and validated in the parse path today.
- The existing per-normalizer invariants (`creation <= uncached` by
  construction for Anthropic/Kimi; `cacheWrite <= input - cached` enforced in
  `normalizeOpenAIUsage`) bound the column against the folded total; the
  migration may additionally pin the invariant in SQL (see the sketch's open
  question).
- `usageEventsEqual` now compares the write bucket, so a transcript replaced by
  a generation with different cache-creation counts replays as a conflict,
  same as any other counter change — consistent, and already the behavior the
  pricing split relies on.

## Decision 3: backfill — none; forward-fill from deployment

### No historical re-parse; repair is not a backfill channel

The column starts NULL on every existing row and fills forward from events
ingested after the migration lands. No migration-time scan, no lazy re-derive,
consistent with ADR 0005 Decision 4 and for the same reasons: transcripts are
provider-owned and routinely rotated, so a re-scan yields a partial,
source-dependent dataset anyway; the write bucket for old events is already
recoverable only where the event's `provider_usage_json` survived (and, for
Codex, conditionally on the CLI's shape), so a backfill would manufacture an
inconsistent mixture that looks authoritative; and the component is a
forward-looking cost/efficiency diagnostic, with historical token/cost totals
complete and unaffected.

One boundary needs stating because the repo has a writer that touches old
events: the legacy attribution repairer refills `provider_usage_json` (and
attribution) from durable transcripts that still match. It must remain a
single-writer path: when a repair does rewrite an event through the normal
parse/normalize path, the event gains the new column exactly as any newly
ingested event would — never via a second, divergent derivation bolted onto
the repairer.

### Alternatives considered

1. **Backfill from `provider_usage_json` at migration time.** Rejected: it is
   a partial dataset dressed as a complete one (pre-0115 rows have no object;
   Codex objects carry the bucket only conditionally), and it moves the fold
   fix into a one-time data migration that can never be re-run against
   rotated transcripts.
2. **Lazy per-event derivation on read.** Rejected: reintroduces the
   provider-JSON coupling Decision 1 rejects, now on the read path with no
   bound.

### Consequences

- Historical scopes render the component as unknown (mixed old/new scopes are
  nil under the coverage rule), which the UI already knows how to display from
  the timing work.
- The deployment is a plain schema change plus forward ingestion; no batch
  job, no replay risk.
- Over time the unknown window recedes naturally as fresh events dominate every
  bounded range.

## Decision 4: API surface — a fifth, optional component; totals stay coherent

### `cacheCreationInputTokens` joins the totals, trend, and request-log DTOs as an additive nullable field

The component is surfaced where the other token components are surfaced, with
identical nil semantics:

- **`UsageTotalsResponse`** gains `cacheCreationInputTokens *int64` ("Cache
  creation (cache write) input tokens; subcomponent of uncachedInputTokens.
  Null when not fully known."). Because session usage, the global summary, the
  per-harness/per-model blocks, and the runtime stats bar all embed this
  totals type, one DTO addition propagates the component to every totals
  surface at once. The domain `UsageMetricTotals` gains the same field.
- **`UsageTrendBucketResponse`** gains the same field as a fifth series.
  Absent buckets keep their explicit zeros (the sum over an empty bucket is a
  known zero); a bucket whose events' write counts are not fully known keeps
  the component nil, exactly like its other components. The current "no
  separate cache-creation series is derived" comments on
  `GlobalUsageTrend`, `UsageTrendBucketResponse`, and `Trend` flip with the
  change.
- **`UsageRequestLogEntryResponse`** gains the field too: the request log is
  the per-event fidelity view, and this is the per-event fact (the PR #19
  review asked for the split at the API boundary; the entry row is where that
  is most literal). The row's *display* does not change in this slice — see
  Decision 5.

Coherence rules, held fixed by design:

- `inputTokens = cachedInputTokens + uncachedInputTokens` — unchanged; the
  write bucket is a declared subcomponent of uncached input, not a fifth
  addend.
- `processedTokens = inputTokens + outputTokens` — unchanged; cache creation
  is never added to it.
- `cacheHitRate = cachedInputTokens / (cachedInputTokens + uncachedInputTokens)`
  — unchanged, per the issue and because it is correct: a cache write is a
  genuine non-hit, and redefining the published metric mid-flight would break
  every consumer of the number.

Naming: `cacheCreationInputTokens`, following the issue's vocabulary and
Anthropic's public name; the description names the OpenAI synonym ("cache
writes") so no consumer has to discover it. The alternative
`cacheWriteInputTokens` was considered and set aside: pricing's internal
`cacheWrite` names stay as-is (implementation vocabulary), but the public
surface follows the product term the issue and statistics page use.

### Alternatives considered

1. **Exclude cache writes from the hit-rate denominator.** Rejected: changes
   the published definition (issue says unchanged) and silently rewrites every
   historical rate already consumed downstream. The new card explains heavy
   cache-warming periods that depress the rate.
2. **Break `uncachedInputTokens` into `freshInputTokens` + `cacheCreationInputTokens`
   and deprecate the folded field.** Rejected: a breaking rename across every
   totals surface for zero informational gain — the folded field remains the
   billing-relevant "everything that was not a read", and the invariant CHECK
   and every stored row already speak it.
3. **Trend-only exposure, totals untouched.** Rejected: the summary card is the
   primary product surface (issue decision 5); a trend series without the
   totals component would force clients to re-aggregate buckets.

### Consequences

- Additive-only change: every existing field keeps its name, type, and
  meaning; `minimum: 0` and nullability match the sibling counters.
- `npm run api` regenerates `openapi.yaml` and `frontend/src/api/schema.ts`;
  both commit together with the Go changes per the repo's API contract rules,
  and the spec drift/parity tests cover the Go side.
- The request-log DTO gains a field its UI ignores for now — harmless, and the
  hook-up point for any later row-level polish.

## Decision 5: UI — a component card, a trend series, eight locales, and one unknown marker

### The statistics page shows cache creation as its own component; unknown stays visibly unknown

- **Statistics page** (`UsageStatisticsView`): a new component card for cache
  creation beside the existing input/cached/uncached/output cards, reading the
  shared totals block. Under the coverage rule the card renders the unknown
  marker — not 0 — for scopes where any event lacks the bucket, which is the
  pre-deployment window for every deployment.
- **Trend chart** (`UsageTrendChart`): a fifth series for the new component.
  Buckets render nil as a gap/unknown per the chart's existing nil handling,
  never as a zero line.
- **Request log rows**: unchanged in this slice (Decision 4 exposed the DTO
  field; the row keeps its current cached sub-count display). A later slice
  may render it; this one does not argue for it.
- **Session inspector / runtime stats bar**: inherit the totals block without
  layout change; the strip keeps its seven figures and gains nothing in this
  slice.
- **i18n**: card title, short description, and trend legend strings are added
  to all eight locales (`de`, `en`, `es`, `fr`, `ja`, `ko`, `pt-BR`,
  `zh-CN`); the renderer locale-coverage test enforces parity. Copy must not
  imply a provider distinction — the component exists for all sources (only
  its unknown-ness is per-source history).
- **Unknown rendering**: reuse the timing work's unknown marker, visually
  distinct from zero. A nil component is "not collected (pre-deployment or
  unavailable)", a zero is "reported zero".

### Alternatives considered

1. **Fold the number into the existing input card as a tooltip.** Rejected:
   the entire point of the issue is that cache creation is invisible; a
   hover-only figure on the very card that still absorbs it repeats the fold
   at the UI layer.
2. **Show cache creation as a percentage of input.** Deferred: a cheap
   derivation, but a ratio without a coverage story invites misreading during
   the all-unknown window; it can ride along later without a schema change.
3. **Per-source badges on the card.** Deferred: sources are already a filter;
   badges would duplicate it.

### Consequences

- The statistics page finally answers "how much did cache writes cost me in
  tokens" for every certified source, not just Anthropic.
- The unknown window is honest and self-explanatory once the marker copy lands
  in all locales.
- No new UI primitives: one card, one series, locale strings, and the existing
  marker.

## Decision 6: compatibility — additive DTO change only; no consumer breaks

### Old payloads never change; new fields are ignorable

The change adds optional JSON members to existing response types and touches
no existing member's name, shape, or semantics. Concretely:

- **HTTP clients** that ignore unknown fields are unaffected; clients that
  type off the generated `frontend/src/api/schema.ts` get the new optional
  members after regeneration, and TypeScript's optionality makes the migration
  compile-time visible rather than runtime-breaking.
- **Summary/trend consumers** (statistics page hooks, session inspector,
  runtime stats bar) read the new component only where they display it;
  nothing forces a read. The runtime stats bar's totals block gains the field
  without any strip change.
- **CLI**: there are no CLI commands that print usage totals today (verified
  against `backend/internal/cli/`), so no hand-mirrored CLI DTO changes in
  this slice. If a usage command lands later, its mirror picks the field up
  under the repo's manual-mirror convention.
- **Docs that encode the fold** — `UsageTokenMetrics`, `GlobalUsageTrend`,
  `UsageTrendBucketResponse`, and `Trend`'s doc comments, which all currently
  state that cache creation is not separable — are updated in the same slice
  so the documentation cannot contradict the wire.

### Alternatives considered

1. **A versioned endpoint or `v2` summary payload.** Rejected: versioning a
   surface to add an optional field is overhead the additive rule exists to
   avoid.
2. **A dedicated cache-creation endpoint.** Rejected: it would fork the
   totals model, force double reads, and drift from the shared coverage
   rules.

### Consequences

- No migration or flag for consumers; deployment ordering does not matter
  (frontend and daemon can update independently in either order).
- The generated-artifact discipline (`npm run api`, commit
  `openapi.yaml` + `schema.ts` with the Go change, spec drift tests) is the
  only extra work, and it is already the repo's standard flow.

## Storage sketch summary

One append migration, `0138_usage_cache_creation_split.sql` (column names
indicative; the slice owns the exact migration and `npm run sqlc`
regeneration):

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE model_usage_events
    ADD COLUMN cache_creation_input_tokens INTEGER
    CHECK (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE model_usage_events DROP COLUMN cache_creation_input_tokens;
-- +goose StatementEnd
```

- The standalone non-negative CHECK is safe under `ADD COLUMN` (every existing
  row's value is NULL, which passes). Whether the bundled SQLite build also
  accepts the additive invariant as a cross-column CHECK
  (`cache_creation_input_tokens IS NULL OR uncached_input_tokens IS NULL OR
  cache_creation_input_tokens <= uncached_input_tokens`) in an `ADD COLUMN`,
  and enforces it for later writes, is an open item for the slice; the
  normalizers already guarantee the invariant for everything ingested, so the
  SQL-level pin is defense in depth, not correctness.
- No new index (no filter or group on the column), no new trigger (the
  existing `usage_bindings` CDC signal already fires once per chunk, and the
  column rides the same event write in `ApplyUsageChunk`), no change to
  `provider_usage_json` (it keeps storing the verbatim object pricing reads).
- Aggregate queries add the standard pair per scope:

```sql
CAST(COALESCE(SUM(mue.cache_creation_input_tokens), 0) AS INTEGER)
    AS cache_creation_input_tokens,
CAST(COUNT(mue.cache_creation_input_tokens) AS INTEGER)
    AS known_cache_creation_token_count,
```

  with the Go-side gate `knownCacheCreationCount == eventCount` producing the
  nullable component, mirroring `scopeTokenMetrics` for the existing counters.

## Re-scope of the follow-up implementation slice

The approved slice ("surface the cache-creation split") is:

1. **Migration + storage.** `0138` append migration as sketched, sqlc query
   and store regeneration (`npm run sqlc`), aggregate `SUM`/`COUNT` pairs and
   the coverage gate; resolve the cross-column CHECK open item and pin the
   decision in the migration comment.
2. **Ingestion.** Emit `CacheCreationInputTokens` from the Claude, Kimi, and
   Codex parse paths (value already computed and validated in each), add the
   field to the domain event/`UsageTokenMetrics` (joining `usageEventsEqual`
   automatically), keep `provider_usage_json` and pricing untouched.
3. **Read models + API.** `UsageMetricTotals` / `UsageTrendBucketTotals` /
   `GlobalUsageTrend` fields, `usageTotalsResponse` mapping, DTO fields on
   totals/trend/request-log, updated doc comments, `npm run api` regeneration
   with spec drift + route parity tests.
4. **UI + i18n.** Component card, trend series, eight locale files, unknown
   marker reuse; request-log row display unchanged.
5. **Tests.** Per-source ingestion (Anthropic known-zero, Kimi counter, Codex
   delta and derived paths, malformed-record handling), nil semantics
   (pre-capture rows, mixed-coverage scopes, absent buckets), aggregation
   coverage, DTO/regeneration drift, and the locale coverage suite.

Out of scope, deliberately: any backfill (Decision 3), hit-rate redefinition
(Decision 4), request-log row display (Decision 5), tier-split columns
(Decision 2, deferred), and any change to pricing or `provider_usage_json`.

## Open items the implementation slice must verify

1. Whether the bundled SQLite accepts and enforces the cross-column
   `cache_creation_input_tokens <= uncached_input_tokens` CHECK under
   `ADD COLUMN`; if not, enforce at the write path and document the gap in the
   migration comment.
2. The `Down` migration's `DROP COLUMN` support on the bundled driver; fall
   back to the 0115-style documented no-op if the driver predates it.
3. The legacy repairer's routing: confirm a repaired event flows through the
   normal normalize path (so the column fills coherently) and add a test that
   repair never writes the column without rewriting the counters it derives
   from.
4. Codex coverage edge: a rollout whose cumulative vector decodes a missing
   `cache_write_input_tokens` as 0 yields a known zero, matching how every
   other Codex counter already behaves — confirm the slice wants that
   documented rather than special-cased, and state it in the parser comment.
5. UI copy review for the unknown marker and card description across all eight
   locales, especially the "subcomponent of uncached input" phrasing.

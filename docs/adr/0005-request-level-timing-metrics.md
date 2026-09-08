# 5. Request-level timing metrics for chat and TUI sessions

Date: 2026-09-08
Status: Proposed

## Context

The usage statistics work (#1-#9) needs request-level timing metrics across
both session modes: per-request/turn first-token latency, LLM elapsed time,
tool-call elapsed time, output tok/s, and per-session aggregates (rounds,
steps). The concept strip this feeds is a session runtime stats bar:

> 11 rounds · 117 steps | LLM 20m17s · tool calls 8m23s | first token avg 4s ·
> 84 tok/s | cache hit 97% | total 6.3M tok…

Today the V1 usage pipeline certifies token and cost facts only. It watches
provider-owned JSONL transcripts (`claude_main`, `claude_subagent`,
`codex_rollout`, `kimi_wire`), parses them into `model_usage_events`, and prices
those events. No timing data is captured anywhere. The certified-sources
contract is deliberate and load-bearing: only those four native artifact shapes
(`UsageSourceKind` in `backend/internal/domain/usage.go`) and only those three
harnesses (`SupportedHarness`: Claude Code, Codex, Kimi) are ingested;
everything else is skipped, never guessed.

The two session modes have different raw material for timing:

- **Chat mode** is AO-driven. `conversation_turns` persist `requested_at`,
  `started_at`, `completed_at`; `conversation_activities` and
  `conversation_messages` persist `created_at`/`updated_at` plus a terminal
  status. AO is the party talking to the provider, so it is the first-party
  observer of when work started and when content arrived. Chat-mode timing is
  therefore mostly a read-model over tables that already exist.
- **TUI/native mode** runs the agent's own terminal UI. AO is not on the
  request path; its certified facts come from the transcripts the native CLI
  writes, which carry per-record timestamps but were never parsed for timing.

This ADR decides the five open questions so the implementation slices (#7
"ingest timing metrics", #8 "duration and first-token columns in the request
log", #9 "session runtime stats bar") can proceed. It is the human
architecture-review gate for those slices.

The repository rules this decision must respect:

- **Durable facts, derived display.** Aggregates and any display state are
  computed at read time in the service layer (`docs/architecture.md`,
  "Durable Facts, Derived Status"). Only facts that cannot be reconstructed
  later are persisted.
- **No parallel CDC emission.** Change events come from DB triggers into
  `change_log`; the daemon does not emit its own.
- **Unknown is nil, never zero.** `model_usage_events` nullable counters carry
  this today: a NULL column is an uncollected metric, a stored zero is a known
  zero. Timing must inherit the same semantics.
- **Certified sources only.** Timing for a non-certified harness is skipped
  exactly as tokens/cost are skipped today.

## Definitions used throughout

- **Request.** Chat mode: one AO-dispatched `conversation_turn` (a
  user/automation prompt plus the agent work it caused). Native/TUI mode: one
  certified model call, i.e. one `model_usage_events` row (an assistant message
  completion that carried provider usage). This matches the existing request
  count semantics: "count of usage events for now".
- **Step.** Native mode: the same unit as a request (a `model_usage_events`
  row). Chat mode: one assistant `conversation_messages` row inside a turn.
  A chat turn with three assistant replies is one round with three steps.
- **Round.** Chat mode: one prompt-bearing `conversation_turn` (a turn the user
  or automation started; daemon-only turns such as compaction do not count).
  Native mode: a maximal run of steps with no human prompt between them; the
  group ordinal is assigned during ingestion from transcript prompt markers (see
  Decision 2), and only root-generation sources contribute rounds.
- **Tool call.** Chat mode: a terminal `conversation_activities` row inside a
  turn. Native mode: the tool phase between an assistant completion record and
  the next user `tool_result` record, measured as one transcript-clock interval
  (see Decision 1). Individual per-tool granularity is not certified in V1.
- **LLM elapsed.** The wall time a model call or turn spent generating, as
  defined per mode in Decisions 1-2.
- **First-token latency.** Wall time from the user sending the message to the
  first response message being received (用户发送消息 → 首次收到回传消息), as defined
  per mode in Decision 3.

## Decision 1: data sources per mode

### Chat mode: read-model over the conversation tables; no new capture

Chat-mode timing is derived at read time from facts AO already persists as a
first-party observer:

- a turn's wall time is `completed_at - started_at` (only when both are set and
  the turn is terminal; `requested_at` to `started_at` separately records queue
  latency);
- a tool call's elapsed time is its activity's terminal `updated_at` minus its
  `created_at`;
- a turn's LLM elapsed is its wall time minus the sum of its tool-call elapsed
  times (a remainder: it includes provider queueing and any inter-tool pauses,
  and is treated as unknown when the subtraction is negative);
- rounds and steps count `conversation_turns` and assistant
  `conversation_messages` per the definitions above.

No new chat-mode storage and no new chat-mode ingress are introduced. The
conversation tables already carry the durable timestamps; persisting derived
durations would violate the durable-facts rule.

### Native/TUI mode: transcript event timestamps, parsed at ingestion

Timing for certified native sources is derived from the same JSONL artifacts
tokens/cost already come from, during the same ingestion pass, and persisted as
new facts. The derivation rules are per source but share one certification
rule:

> A duration is recorded only when both bounding timestamps are present in the
> certified artifact and the boundary semantics are documented for that source.
> Anything else stays NULL. Durations are transcript-clock intervals between
> native record timestamps, not stopwatch observations; that caveat is part of
> the metric's meaning.

For Claude Code, whose transcript alternates assistant completions (with usage)
and user `tool_result` records, this yields for each assistant usage record:
LLM elapsed = assistant record timestamp minus the preceding user/prompt record
timestamp; tool elapsed = the following `tool_result` record timestamp minus
the assistant record timestamp. Codex rollout and Kimi wire records carry
per-record timestamps too; their exact boundary records are decided by the
implementing slice (#7) with parser tests, under the same rule. Records that
predate the capture, or whose needed neighbor is missing (a subagent transcript
that starts mid-conversation), produce NULL fields rather than estimates.

### What is explicitly out of scope

- **Hook ingress is not a timing source in V1.** The usage collector's hooks
  (`RecordHook` on `session-start`/`session-end`/`subagent-stop`) register and
  activate sources; they carry no per-request timing payload for any certified
  agent, and they fire at turn/tool boundaries, not at LLM request boundaries.
  Adding timing to hooks would invent payloads the agents do not send. Hooks
  keep their current job: source registration and route hints.
- **Terminal-mux byte observation is not a first-token source.** TUI rendering
  and drawing make "first observed output byte" a different event than "first
  model token", and it is not replayable from durable artifacts.
- **Non-certified harnesses get no timing facts**, matching the token/cost
  pipeline today. Their sessions render timing sections as unknown.
- **No transcript re-scan** (see Decision 4).

### Alternatives considered

1. **Hook ingress as primary timing source** (measure wall clock when native
   hooks fire). Rejected: no certified agent's hook brackets one LLM request
   with a start and a first-byte event today; stop-hook wall time includes user
   think time; and hook observations are not recoverable after a crash, unlike
   transcript records.
2. **Derive everything at read time from retained transcripts.** Rejected:
   transcripts are provider-owned, rotated and rewritten, and are not a query
   surface; read-time access would couple UI reads to filesystem state and to
   parser correctness on every page load. Facts that require the transcript are
   therefore captured at ingestion (Decision 2).
3. **Both transcript derivation and hook ingress.** Deferred: transcript
   derivation covers the certified sources; hook timing earns its complexity
   only when an agent exposes a certified stream lifecycle. Tracked as a
   follow-up, not built speculatively.

### Consequences

- Chat-mode timing needs no migration and no new writer, so the slice that
  consumes it (#9) is pure read-model service work plus UI.
- Native timing correctness depends on parser fidelity per source. The
  transcript-clock caveat must be surfaced honestly in the UI/API naming
  (durations are "LLM elapsed", not "model-reported latency").
- Because facts are captured at ingestion, later transcript rotation or
  deletion does not destroy timing (see Decision 5).
- First-token latency is derived per source from the user/prompt record to the
  first assistant response record, and is constrained by whether the certified
  artifact records both anchors (Decision 3).

## Decision 2: storage shape

### New child table(s); do not extend usage events or conversations; never persist session aggregates

Timing gets its own migration with a child table of `model_usage_events`,
written in the same chunk transaction that writes the events:

```sql
-- One timing row per native certified request (one model_usage_events row).
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
```

Notes on the sketch (column names are indicative; #7 owns the exact migration):

- The table is keyed 1:1 by `model_usage_events.id`, so the request log (#8)
  is a LEFT JOIN and per-session aggregation is a grouped read. No event
  columns change, so the certified write-once event contract, its replay
  dedupe comparison, cost-candidate scans, and legacy attribution repair are
  untouched.
- The timing row is written atomically with its event inside `ApplyUsageChunk`.
  Timing derivation is deterministic given identical records, so replay and
  transcript replacement re-derive the same values; when a replacement
  generation legitimately changes a timestamp, the child row is upserted to
  match the new certified generation (the parent row identity does not change
  across a rehome).
- Chat mode needs no timing table at all (Decision 1), so the only new storage
  is this native child table.

### How rounds and steps are defined

Steps need no storage beyond the events themselves (count of rows). Rounds are
persisted as `round_seq` per native event row because the round boundary lives
only in the transcript: a human prompt between two steps. The parser tracks the
current round ordinal in its durable parser state and stamps each emitted event
with it. Round counting at the session level uses only root-generation sources
(`subagent_id = ''`: `claude_main`, root Codex rollout, Kimi main); subagent and
child sources contribute steps, time, and tokens but never add rounds of their
own, so a session's "11 rounds" counts user exchanges, not agent-internal
spawns.

`round_seq` is a durable grouping fact assigned from certified transcript
markers (a prompt record between steps) -- the same category as parser cursor
state -- not a derived display value, so storing it does not violate the
durable-facts rule. Session aggregates (rounds count, summed LLM/tool time,
average first token, average tok/s, token/cost totals) are never stored; they
are computed at read time in the service layer from event rows (native) or
conversation rows (chat).

### Alternatives considered

1. **Add timing columns to `model_usage_events`.** Rejected. That table is the
   certified token/cost fact store with immutable write-once semantics, CAS
   replay, and exact-field dedupe comparisons. Timing columns would either join
   the replay comparison (making a harmless parser change a replay conflict) or
   silently diverge from it, and the table would lose its clean meaning as
   "certified token/cost facts".
2. **One unified request table covering both modes.** Rejected for V1: chat
   timing is derived, not stored, so a unified table would only ever hold
   native rows and would invite fabricating chat token counts per request that
   are not certified. If a later slice needs chat rows in the request log, that
   is a new decision.
3. **Persist session runtime aggregates.** Rejected: they are display state and
   violate the repo rule. Everything the stats bar shows is derivable from
   event rows plus the conversation model.

### Consequences

- The request log and stats bar read paths are cheap joins over certified
  facts; there is no aggregate table to keep consistent.
- The migration is additive and small. Backfilling is explicitly not done
  (Decision 4), so the table starts empty and fills forward.
- Parser state grows by a bounded round/timing accumulator; it is already
  versioned JSON in `usage_sources.parser_state_json`, so old sources decode
  with the field absent and simply start timing collection at their cursor.

## Decision 3: tok/s and first-token definitions, and unknowns

### Unknowns are nil, never zero

Every timing field is NULL when it cannot be certified (SQL NULL; `nil` in Go
DTOs; an explicit "unknown"/"-" marker in the UI). Zero is only ever a known
zero (e.g. an output-token count of zero, or a measured sub-millisecond delta
rounded to 0 ms). This matches the existing usage semantics exactly.

### Durations

Stored as non-negative INTEGER milliseconds. A NULL duration means the boundary
record was missing or predates capture. A request whose LLM delta rounds to
0 ms (clock granularity) is treated as unknown for rate purposes, never as
"infinitely fast".

### First-token latency

Per request: **wall time from the user sending the message to the first response
message being received** (用户发送消息 → 首次收到回传消息). This is the definition
used everywhere below; it deliberately measures the round trip the user feels
(request sent → first reply received), not the provider's internal first-byte
time.

- **Chat mode:** align to the same semantics as native mode: user-send time to
  first content received. The durable read model has `conversation_turns.requested_at`
  (when the user's message was requested/sent) and the turn's first assistant
  `conversation_messages`/`conversation_activities` row (when the first reply
  content was received). First-token = first assistant content row `created_at`
  minus `requested_at`. Unlike the previous draft, `started_at` is not the
  start anchor: `requested_at` records user send time, which is what the
  definition calls for; `started_at` to `completed_at` still backs the turn
  wall-time metric (Decision 1). This is only certified if the chat driver
  creates the assistant row when streaming content arrives rather than at final
  completion; the implementing slice must confirm that stamping and, if needed,
  make the minimal change so the durable row records first-content arrival.
  Until that is confirmed, chat first-token stays NULL.
- **Native/TUI mode:** **derivable, not NULL.** The certified transcripts
  record both a user/prompt record timestamp and the first assistant response
  record timestamp, so first-token is derived per source from those two
  timestamps under the same per-source certification rule as the other
  durations (Decision 1). For Claude Code, the user message record precedes the
  first assistant completion record; for Codex rollout and Kimi wire, the
  prompt/turn record precedes the first assistant response record. The exact
  boundary records per source are fixed by #7 with parser tests. A record whose
  prompt anchor is missing (a subagent transcript that starts mid-conversation,
  or a session resumed from a mid-stream cursor) yields NULL first-token, never
  an estimate. First-token is the gap to the **first** assistant record after
  the prompt, not the gap to the prompt's own completion, and never a
  completion-to-completion interval.

Session average: arithmetic mean over the requests whose first-token value is
known, reported with a coverage figure (e.g. "12 of 15 requests"). A session
with no known values reports unknown, never zero.

### Output tok/s

Per request: `output_tokens / (llm_ms / 1000)`, computed at read time and never
stored. It is defined only when `llm_ms` is known and positive AND the event's
output tokens are known; otherwise it is unknown. A known zero output-token
count with a known positive duration yields a known zero rate.

Session average output tok/s: `sum(output_tokens) / sum(llm_seconds)` over the
requests that carry both facts -- the aggregate of numerators over the
aggregate of denominators, not the mean of per-request rates, so a tiny request
does not weight a large one equally. Sessions with no covered request report
unknown.

### Alternatives considered

1. **Default unknown durations to 0 for display.** Rejected: it would draw an
   empty bar for an unmeasured request and corrupt aggregates.
2. **Arithmetic mean of per-request tok/s for the session figure.** Rejected:
   it weights short requests disproportionately; the ratio-of-sums definition
   matches how the token totals themselves are summed.
3. **Derive native first-token from the gap between a prompt record and the
   next assistant record.** Accepted as the chosen definition (see above), with
   one guard: it is the gap to the **first** assistant record after the prompt,
   and it measures the transcript-clock round trip the user feels. It is not
   labeled as the provider's internal first-byte time; that remains unavailable
   and is never derived.

### Consequences

- "first token avg" renders for both modes where the facts exist: chat sessions
  that stamp first-content arrival (measured from user send time), and native
  sessions where the transcript records a user prompt followed by a first
  assistant response. Sessions or requests missing the needed anchor render the
  explicit unknown marker. The honest hole is visible and deliberate.
- tok/s and first-token are never stored, so they cannot go stale and need no
  backfill.
- The UI must have one unknown marker for "not collected" that is visually
  distinct from a zero value.

## Decision 4: backfill policy

### Timing starts at deployment; no historical transcript re-scan

Timing facts are collected only for events ingested after the migration lands.
Existing `model_usage_events` rows and their sources are not re-parsed.

Rationale:

- A re-scan can only produce the transcript-clock duration proxies, never
  first-token latency (that was never captured), so historical backfill buys a
  partial metric at the cost of re-parsing every retained transcript.
- Provider-owned transcripts are routinely rotated, archived, and rewritten
  (the collector already handles replaced Codex rollouts and archived
  sessions), so a re-scan would yield an inconsistent, source-dependent subset
  anyway -- the same dataset forward-fill produces, with more engineering and
  more replay risk.
- Timing is a forward-looking diagnostic; historical sessions are rarely
  reopened, and their token/cost facts are already complete and unaffected.

A bounded "re-derive timing for currently retained sources" pass may become a
follow-up if a real need appears; it is not part of #7.

### Alternatives considered

1. **Full historical re-scan at migration time.** Rejected above.
2. **Lazy per-session re-derivation on first view of an old session.** Rejected:
   it couples read latency and correctness to transcript availability and parser
   state at arbitrary later times.

### Consequences

- The timing table starts empty; older sessions show token/cost sections with
  timing unknown (the #9 stats bar's "timing-less session" test case).
- No migration-time batch job, so the deployment is a plain schema change plus
  forward ingestion.
- The request log's duration columns (#8) are empty for pre-deployment rows and
  render the unknown marker, which the nil-preserving DTO already requires.

## Decision 5: retention and interaction with change_log/CDC

### Timing rows live and die with their usage events; no new CDC

- **Retention.** Timing facts cascade with their parent: `event_id` is an
  `ON DELETE CASCADE` foreign key to `model_usage_events`, which already
  cascades through `usage_sources`/`usage_bindings` to the session. There is no
  separate time-based retention in V1, mirroring `model_usage_events` today. If
  a future slice adds retention pruning to usage events, timing rows are pruned
  by the same cascade/statement; there is no aggregate table to orphan.
- **CDC.** No new triggers and no new `change_log` event types. `model_usage_events`
  deliberately has no insert trigger today; `ApplyUsageChunk` invalidates the UI
  once per chunk by touching the binding's `updated_at`, which fires the
  existing `usage_bindings` trigger and emits one `session_updated`. The timing
  child row is written inside that same transaction, so it inherits that single
  invalidation with zero additional `change_log` volume. Adding a timing trigger
  would flood the log with one event per model call.
- **Chat mode adds no storage**, so it adds no triggers either. The derived
  stats bar for a chat session refreshes on the existing turn/message/activity
  triggers (`conversation_turns_cdc_update`, activity/message insert and
  revision triggers), which already emit `session_updated`.
- `change_log.event_type` keeps its existing CHECK allowlist. The 0006
  precedent is that widening the allowlist means rebuilding the table and
  re-declaring every existing trigger, so no new event type is introduced for
  timing; clients refetch the affected read model on the existing
  `session_updated` signal.

### Alternatives considered

1. **Add a `model_usage_event_timing` CDC trigger.** Rejected: one `change_log`
   row per model call would swamp the log and the UI stream; nothing needs the
   per-row signal.
2. **Add a dedicated `timing_updated` event type.** Rejected: requires the
   full allowlist rebuild for a label nothing consumes.
3. **Prune timing independently of usage events.** Rejected: the facts are only
   meaningful joined to their event, and cascade delete keeps them coherent.

### Consequences

- Retention of native timing is bounded by session lifetime, exactly like the
  token/cost facts it annotates. A deleted session's timing disappears with it.
- Because timing is captured at ingestion, provider transcript rotation after
  ingestion (archived Codex sessions, Claude project cleanup) cannot erase it,
  which is a real durability win over any read-time transcript derivation.
- The stats UI stays on the existing SSE/refetch contract; the timing slices
  introduce no new streaming surface.

## Storage sketch summary

Native/TUI mode only:

- `model_usage_events` (unchanged) -- certified token/cost facts, one row per
  request.
- `model_usage_event_timing` (new) -- `event_id` PK/FK, `round_seq`, nullable
  `llm_ms`/`tool_ms`/`first_token_ms`, `created_at`; written atomically with
  its event in `ApplyUsageChunk`.

Chat mode: no new tables. Timing and round/step figures are derived at read
time from `conversation_turns`, `conversation_messages`, and
`conversation_activities`.

Session-level aggregates (rounds, steps, summed LLM/tool time, average
first-token, average output tok/s, token/cost totals): never stored; computed
in the service layer per mode from the facts above.

## Re-scope of the follow-up slices

These decisions change the blocked slices' scope as follows:

- **#7 "ingest timing metrics".** Scope narrows on chat and grows on native
  ingestion: chat requires no migration and no persistence -- the slice adds a
  read-model derivation over the conversation tables (round/step/tool/turn
  figures) to the service layer. The storage work is the native-only
  `model_usage_event_timing` migration plus sqlc queries, per-source transcript
  timing derivation under the Decision 1 certification rule, atomic child-row
  writes in `ApplyUsageChunk`, forward-fill only, nil semantics, and
  deterministic-replay tests. **First-token is now part of the native
  derivation**, from the user/prompt record timestamp to the first assistant
  response record timestamp per source; #7 fixes the exact boundary records
  with parser tests and writes `first_token_ms`. #7 must also confirm the chat
  first-content stamping question from Decision 3 and either make the minimal
  durable change or leave chat first-token NULL. Public DTOs still land with
  the consuming UI slices (#8/#9).
- **#8 "duration and first-token columns in the request log".** Shape unchanged;
  semantics clarified: request-log rows are usage events, so the duration column
  is `llm_ms` joined from `model_usage_event_timing`, and the first-token column
  is `first_token_ms` joined from the same table. **Native rows now carry
  first-token values** where the transcript recorded the prompt and first
  response; rows whose prompt anchor is missing render the unknown marker.
  Nil-preserving DTO and unknown-marker rendering stay as specified.
- **#9 "session runtime stats bar".** Becomes a mode-aware read model: for chat
  sessions, rounds/turns, steps, tool time, and LLM time derive from the
  conversation tables; for native sessions they aggregate certified events and
  their timing rows; token/cost sections come from the usage-event totals as
  today. **The "first token avg" strip is populated in both modes** where the
  anchors exist, and shows the coverage figure (known-count of requests) as in
  Decision 3. Sessions without timing facts degrade to token/cost with unknown
  timing markers. Where a chat-mode session also carries certified usage events
  (a chat driver running a certified CLI that writes transcripts), the slice
  must define precedence between the two fact families in tests; that is a #9
  concern, not a storage one.

## Open items the implementation slices must verify

1. Whether the chat driver stamps assistant rows at first-content arrival (D3);
   if not, whether to add that stamp, so chat first-token (measured from
   `requested_at`) is certifiable.
2. Exact per-source boundary records for the first-token interval (user/prompt
   record → first assistant response record) in Claude, Codex, and Kimi
   transcripts under the D1 certification rule.
3. Exact per-source boundary records for LLM/tool intervals in Codex and Kimi
   transcripts under the D1 certification rule.
4. Round-prompt marker detection per source (Claude Code text user messages
   versus `tool_result` user messages, Codex turn records, Kimi equivalents).

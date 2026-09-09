package domain

import "time"

// SessionRuntimeTimingAggregate is one native-mode storage rollup for the
// per-session runtime statistics read model (timing ADR #9). It is computed at
// read time over certified usage events and their timing rows and is never
// persisted. StepCount counts every event (subagent sources included);
// RoundCount counts distinct (root source, round) pairs because only
// root-generation sources (subagent_id = ”) contribute rounds. Durations are
// transcript-clock intervals captured at ingestion (ADR Decision 1); a nil
// duration is an uncollected metric, never a known zero. RateOutputTokens and
// RateLLMMS are the ratio-of-sums numerator and denominator for the session
// average output tok/s (ADR Decision 3): they cover exactly the events that
// carry both a positive LLM elapsed and known output tokens.
type SessionRuntimeTimingAggregate struct {
	StepCount        int64
	TimingRowCount   int64
	RoundCount       int64
	LLMMSTotal       *int64
	ToolMSTotal      *int64
	FirstTokenMSSum  *int64
	FirstTokenKnown  int64
	RateOutputTokens *int64
	RateLLMMS        *int64
}

// ConversationRuntimeTurnFact is one chat-mode turn's runtime facts, aggregated
// per turn so the service can apply the ADR's per-turn LLM-remainder rule. The
// turn belongs to the session's active branch lineage. PromptBearing marks a
// turn the user or automation started; daemon-only turns (compaction,
// provider-adopted resumes) never count as rounds. FirstTokenDeltaMS is the
// first-token interval (ADR Decision 3) from the turn's requested_at to its
// earliest assistant content row; nil means no assistant content row exists yet
// or the content predates the request (clock skew), never a zero. ToolMS is
// the sum of the turn's terminal activities' elapsed (updated_at - created_at),
// and ToolKnown reports whether any terminal activity contributed.
type ConversationRuntimeTurnFact struct {
	TurnID            string
	State             TurnState
	RequestedAt       time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	FirstTokenDeltaMS *int64
	ToolMS            int64
	ToolKnown         bool
	AssistantCount    int64
	PromptBearing     bool
}

// FirstTokenCoverage reports how many requests contributed to the session
// first-token average, the ADR Decision 3 coverage figure ("12 of 15
// requests"). Total is the session's request count in the mode's semantics:
// usage events for native sessions, prompt-bearing turns for chat sessions.
type FirstTokenCoverage struct {
	Covered int64
	Total   int64
}

// SessionRuntimeStats is the per-session runtime statistics read model that
// backs the session stats bar (timing ADR #9). Every timing figure is derived
// at read time from the mode's own durable facts — conversation tables for
// chat sessions, model_usage_event_timing for native sessions — and never
// persisted. Token/cost totals and CacheHitRate come from usage events in both
// modes. A nil field is an uncollected metric (pre-deployment history, a
// non-certified harness, or an uncaptured anchor); zero is only ever a known
// zero.
type SessionRuntimeStats struct {
	SessionID             SessionID
	Rounds                *int64
	Steps                 *int64
	LLMMS                 *int64
	ToolMS                *int64
	FirstTokenAvgMS       *int64
	FirstTokenCoverage    FirstTokenCoverage
	OutputTokensPerSecond *float64
	CacheHitRate          *float64
	Totals                UsageMetricTotals
}

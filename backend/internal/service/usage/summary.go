package usage

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// maxRequestLogPageSize bounds the request log page the daemon will return.
const maxRequestLogPageSize int64 = 100

type usageSummaryStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListCompactSessionUsageAggregates(context.Context, domain.ProjectID) ([]domain.CompactSessionUsageAggregate, error)
	ListUsageModelAggregates(context.Context, domain.SessionID) ([]domain.UsageModelAggregate, error)
	GetUsageSessionIncomplete(context.Context, domain.SessionID) (bool, error)
	AggregateUsageSummary(context.Context, *time.Time, *time.Time, string, string) (domain.GlobalUsageAggregate, error)
	ListUsageSummaryDimensions(context.Context, *time.Time, *time.Time, string, string) (domain.UsageSummaryDimensions, error)
	AggregateUsageByModel(context.Context, *time.Time, *time.Time, string, string) ([]domain.UsageModelScopeAggregate, error)
	AggregateUsageByProvider(context.Context, *time.Time, *time.Time, string, string) ([]domain.UsageProviderScopeAggregate, error)
	ListUsageRequestLog(context.Context, *time.Time, *time.Time, string, string, *int64, int64) ([]domain.UsageRequestLogEntry, error)
	AggregateUsageTrend(context.Context, *time.Time, *time.Time, int64, string, string) ([]domain.UsageTrendBucket, error)
	AggregateSessionRuntimeTiming(context.Context, domain.SessionID) (domain.SessionRuntimeTimingAggregate, error)
	ListConversationRuntimeTurnFacts(context.Context, domain.SessionID) ([]domain.ConversationRuntimeTurnFact, error)
}

// SummaryReader derives token and estimated-cost summaries from normalized
// usage events.
type SummaryReader struct{ store usageSummaryStore }

// NewSummaryReader constructs a usage summary reader.
func NewSummaryReader(store usageSummaryStore) *SummaryReader { return &SummaryReader{store: store} }

// ListCompact returns one batch suitable for dashboard cards.
func (r *SummaryReader) ListCompact(ctx context.Context, projectID domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("usage summary store is unavailable")
	}
	rows, err := r.store.ListCompactSessionUsageAggregates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CompactSessionUsage, 0, len(rows))
	for _, row := range rows {
		estimatedCost, err := estimatedCost(row.Cost)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.CompactSessionUsage{
			SessionID: row.SessionID, ProcessedTokens: row.ProcessedTokens,
			Incomplete: row.Incomplete, EstimatedCost: estimatedCost,
		})
	}
	return out, nil
}

// Get returns detailed token and estimated-cost telemetry for one session.
func (r *SummaryReader) Get(ctx context.Context, sessionID domain.SessionID) (domain.SessionUsageSummary, error) {
	if r == nil || r.store == nil {
		return domain.SessionUsageSummary{}, fmt.Errorf("usage summary store is unavailable")
	}
	if _, ok, err := r.store.GetSession(ctx, sessionID); err != nil {
		return domain.SessionUsageSummary{}, err
	} else if !ok {
		return domain.SessionUsageSummary{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}

	models, err := r.store.ListUsageModelAggregates(ctx, sessionID)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	visibleModels := make([]domain.UsageModelAggregate, 0, len(models))
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.ModelID), "<synthetic>") {
			continue
		}
		visibleModels = append(visibleModels, model)
	}
	models = visibleModels
	incomplete, err := r.store.GetUsageSessionIncomplete(ctx, sessionID)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	totals, err := usageTotals(models)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	harnesses, err := harnessUsageSummaries(models)
	if err != nil {
		return domain.SessionUsageSummary{}, err
	}
	return domain.SessionUsageSummary{
		SessionID: sessionID, Incomplete: incomplete, Totals: totals, Harnesses: harnesses,
	}, nil
}

// Global returns the cross-session usage summary over an optional created_at
// range. Nil bounds mean unbounded. The optional source kind and model filters
// narrow the scope to exact matches; empty strings mean unfiltered. RequestCount
// is the count of usage events (token-event granularity); true request-level
// semantics arrive with the timing pipeline. CacheHitRate is cached input
// divided by cached plus uncached input, or nil when either component is
// unknown. Sources and Models are the distinct values present in the same
// filtered scope, for dropdown options.
func (r *SummaryReader) Global(ctx context.Context, from, to *time.Time, source, model string) (domain.GlobalUsageSummary, error) {
	if r == nil || r.store == nil {
		return domain.GlobalUsageSummary{}, fmt.Errorf("usage summary store is unavailable")
	}
	agg, err := r.store.AggregateUsageSummary(ctx, from, to, source, model)
	if err != nil {
		return domain.GlobalUsageSummary{}, err
	}
	dims, err := r.store.ListUsageSummaryDimensions(ctx, from, to, source, model)
	if err != nil {
		return domain.GlobalUsageSummary{}, err
	}
	totals, err := usageTotals([]domain.UsageModelAggregate{
		{Tokens: agg.Tokens, Cost: agg.Cost},
	})
	if err != nil {
		return domain.GlobalUsageSummary{}, err
	}
	return domain.GlobalUsageSummary{
		Totals:       totals,
		RequestCount: agg.EventCount,
		CacheHitRate: cacheHitRate(agg.Tokens),
		Sources:      dims.Sources,
		Models:       dims.Models,
	}, nil
}

// ListRequestLog returns one bounded, newest-first page of usage events over an
// optional created_at range, optionally narrowed by an exact source kind or
// model id. limit is the requested page size and is clamped to
// [1, maxRequestLogPageSize]. A nil beforeID returns the newest page; pass the
// previous page's NextBeforeID to page older. Ordering is by event id
// descending (monotonic with insertion, NULL-safe, stable), so the id cursor
// never drops or repeats a row across pages.
func (r *SummaryReader) ListRequestLog(ctx context.Context, from, to *time.Time, source, model string, beforeID *int64, limit int64) (domain.UsageRequestLogPage, error) {
	if r == nil || r.store == nil {
		return domain.UsageRequestLogPage{}, fmt.Errorf("usage summary store is unavailable")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > maxRequestLogPageSize {
		limit = maxRequestLogPageSize
	}
	rows, err := r.store.ListUsageRequestLog(ctx, from, to, source, model, beforeID, limit)
	if err != nil {
		return domain.UsageRequestLogPage{}, err
	}
	var nextBeforeID *int64
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1].ID
		nextBeforeID = &last
	}
	return domain.UsageRequestLogPage{Items: rows, NextBeforeID: nextBeforeID}, nil
}

// cacheHitRate returns cached input divided by cached plus uncached input. Both
// inputs must be known for a rate to exist; otherwise it is nil.
func cacheHitRate(tokens domain.UsageTokenMetrics) *float64 {
	if tokens.CachedInputTokens == nil || tokens.UncachedInputTokens == nil {
		return nil
	}
	denominator := *tokens.CachedInputTokens + *tokens.UncachedInputTokens
	if denominator == 0 {
		return nil
	}
	rate := float64(*tokens.CachedInputTokens) / float64(denominator)
	return &rate
}

// RuntimeStats returns the per-session runtime statistics read model
// that backs the session stats bar (timing ADR #9). Token/cost totals and the
// cache-hit rate come from usage events in both modes; timing figures are
// derived at read time from the mode's own facts: conversation tables for chat
// sessions, model_usage_event_timing for native sessions. Unknowns are nil,
// never zero: a timing-less session (pre-deployment history, a non-certified
// harness) renders token/cost facts with every timing section unknown.
func (r *SummaryReader) RuntimeStats(ctx context.Context, sessionID domain.SessionID) (domain.SessionRuntimeStats, error) {
	if r == nil || r.store == nil {
		return domain.SessionRuntimeStats{}, fmt.Errorf("usage summary store is unavailable")
	}
	session, ok, err := r.store.GetSession(ctx, sessionID)
	if err != nil {
		return domain.SessionRuntimeStats{}, err
	}
	if !ok {
		return domain.SessionRuntimeStats{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}

	stats := domain.SessionRuntimeStats{SessionID: sessionID}

	models, err := r.store.ListUsageModelAggregates(ctx, sessionID)
	if err != nil {
		return domain.SessionRuntimeStats{}, err
	}
	visible := make([]domain.UsageModelAggregate, 0, len(models))
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.ModelID), "<synthetic>") {
			continue
		}
		visible = append(visible, model)
	}
	totals, err := usageTotals(visible)
	if err != nil {
		return domain.SessionRuntimeStats{}, err
	}
	stats.Totals = totals
	stats.CacheHitRate = cacheHitRate(scopeTokenTotals(visible))

	switch domain.NormalizeSessionMode(session.Mode) {
	case domain.SessionModeChat:
		turns, err := r.store.ListConversationRuntimeTurnFacts(ctx, sessionID)
		if err != nil {
			return domain.SessionRuntimeStats{}, err
		}
		deriveChatRuntimeStats(&stats, turns)
	default:
		agg, err := r.store.AggregateSessionRuntimeTiming(ctx, sessionID)
		if err != nil {
			return domain.SessionRuntimeStats{}, err
		}
		deriveNativeRuntimeStats(&stats, agg)
	}
	return stats, nil
}

// scopeTokenTotals sums one token metric across visible model aggregates with
// the same full-knowledge rule as usageTotals: one uncollected counter makes
// the whole sum unknown.
func scopeTokenTotals(models []domain.UsageModelAggregate) domain.UsageTokenMetrics {
	return domain.UsageTokenMetrics{
		InputTokens:         aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.InputTokens }),
		CachedInputTokens:   aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.CachedInputTokens }),
		UncachedInputTokens: aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.UncachedInputTokens }),
		OutputTokens:        aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.OutputTokens }),
	}
}

// deriveNativeRuntimeStats applies the native/TUI-mode session aggregates (ADR
// Decision 2/3). Steps count every usage event; rounds count only the captured
// root-source round ordinals. Durations sum the known timing rows (SUM skips
// NULLs upstream). The session first-token average is the arithmetic mean over
// known values with its coverage; the session output tok/s is the ratio of
// sums over the events carrying both facts. A session with no events leaves
// every timing figure nil.
func deriveNativeRuntimeStats(stats *domain.SessionRuntimeStats, agg domain.SessionRuntimeTimingAggregate) {
	if agg.StepCount == 0 {
		return
	}
	steps := agg.StepCount
	stats.Steps = &steps
	if agg.RoundCount > 0 {
		rounds := agg.RoundCount
		stats.Rounds = &rounds
	}
	stats.LLMMS = agg.LLMMSTotal
	stats.ToolMS = agg.ToolMSTotal
	if agg.FirstTokenKnown > 0 && agg.FirstTokenMSSum != nil {
		avg := *agg.FirstTokenMSSum / agg.FirstTokenKnown
		stats.FirstTokenAvgMS = &avg
		stats.FirstTokenCoverage = domain.FirstTokenCoverage{Covered: agg.FirstTokenKnown, Total: agg.StepCount}
	}
	if agg.RateOutputTokens != nil && agg.RateLLMMS != nil && *agg.RateLLMMS > 0 {
		rate := float64(*agg.RateOutputTokens) / (float64(*agg.RateLLMMS) / 1000.0)
		stats.OutputTokensPerSecond = &rate
	}
}

// deriveChatRuntimeStats applies the chat-mode session aggregates (ADR Decision
// 1). Rounds count prompt-bearing turns; steps count assistant messages. A
// turn's LLM elapsed is its terminal wall time minus its tool-call elapsed (a
// remainder, unknown when the subtraction is negative); the session figure sums
// the known turns. Tool time sums terminal activities' elapsed and is unknown
// unless at least one terminal activity contributed. First-token is measured
// from the turn's requested_at to its earliest assistant content row; the
// session average is the mean over covered turns with its coverage against the
// prompt-bearing total. A session with no conversation leaves every timing
// figure nil.
func deriveChatRuntimeStats(stats *domain.SessionRuntimeStats, turns []domain.ConversationRuntimeTurnFact) {
	if len(turns) == 0 {
		return
	}
	var steps, rounds, llmTotal, firstTokenSum, covered int64
	var llmKnown, toolKnown bool
	var toolTotal int64
	for _, turn := range turns {
		steps += turn.AssistantCount
		if turn.PromptBearing {
			rounds++
		}
		if turn.ToolKnown {
			toolKnown = true
			toolTotal += turn.ToolMS
		}
		if turn.State.Terminal() && turn.StartedAt != nil && turn.CompletedAt != nil && !turn.CompletedAt.Before(*turn.StartedAt) {
			llm := turn.CompletedAt.Sub(*turn.StartedAt).Milliseconds() - turn.ToolMS
			if llm >= 0 {
				llmTotal += llm
				llmKnown = true
			}
		}
		if turn.PromptBearing && turn.FirstTokenDeltaMS != nil {
			firstTokenSum += *turn.FirstTokenDeltaMS
			covered++
		}
	}
	if steps > 0 {
		stats.Steps = &steps
	}
	if rounds > 0 {
		stats.Rounds = &rounds
	}
	if llmKnown {
		stats.LLMMS = &llmTotal
	}
	if toolKnown {
		stats.ToolMS = &toolTotal
	}
	if covered > 0 {
		avg := firstTokenSum / covered
		stats.FirstTokenAvgMS = &avg
		stats.FirstTokenCoverage = domain.FirstTokenCoverage{Covered: covered, Total: rounds}
	}
}

func usageTotals(models []domain.UsageModelAggregate) (domain.UsageMetricTotals, error) {
	if len(models) == 0 {
		return domain.UsageMetricTotals{}, nil
	}
	var costs domain.UsageCostAggregate
	for _, model := range models {
		if err := mergeUsageCostAggregate(&costs, model.Cost); err != nil {
			return domain.UsageMetricTotals{}, err
		}
	}
	estimate, err := estimatedCost(costs)
	if err != nil {
		return domain.UsageMetricTotals{}, err
	}
	input := aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.InputTokens })
	output := aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.OutputTokens })
	totals := domain.UsageMetricTotals{
		InputTokens:       input,
		CachedInputTokens: aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 { return model.Tokens.CachedInputTokens }),
		UncachedInputTokens: aggregateMetric(models, func(model domain.UsageModelAggregate) *int64 {
			return model.Tokens.UncachedInputTokens
		}),
		OutputTokens:  output,
		EstimatedCost: estimate,
	}
	if input != nil && output != nil {
		processed := *input + *output
		totals.ProcessedTokens = &processed
	}
	return totals, nil
}

// aggregateMetric sums one metric across models. One uncollected counter makes
// the whole sum unknown rather than silently under-reporting it.
func aggregateMetric(models []domain.UsageModelAggregate, selectMetric func(domain.UsageModelAggregate) *int64) *int64 {
	var total int64
	for _, model := range models {
		value := selectMetric(model)
		if value == nil {
			return nil
		}
		total += *value
	}
	return &total
}

func harnessUsageSummaries(models []domain.UsageModelAggregate) ([]domain.HarnessUsageSummary, error) {
	order := make([]domain.AgentHarness, 0)
	grouped := make(map[domain.AgentHarness][]domain.UsageModelAggregate)
	for _, model := range models {
		if _, ok := grouped[model.Harness]; !ok {
			order = append(order, model.Harness)
		}
		grouped[model.Harness] = append(grouped[model.Harness], model)
	}
	out := make([]domain.HarnessUsageSummary, 0, len(order))
	for _, harness := range order {
		rows := grouped[harness]
		totals, err := usageTotals(rows)
		if err != nil {
			return nil, err
		}
		summary := domain.HarnessUsageSummary{Harness: harness, Totals: totals}
		for _, row := range rows {
			modelTotals, err := usageTotals([]domain.UsageModelAggregate{row})
			if err != nil {
				return nil, err
			}
			summary.Models = append(summary.Models, domain.ModelUsageSummary{
				ModelID: row.ModelID, Totals: modelTotals,
			})
		}
		out = append(out, summary)
	}
	return out, nil
}

func estimatedCost(raw domain.UsageCostAggregate) (*domain.EstimatedCost, error) {
	if err := validateUsageCostAggregate(raw); err != nil {
		return nil, err
	}
	if raw.EventCount == 0 {
		return nil, nil
	}
	coverage := domain.EstimatedCostCoverageComplete
	total := raw.PricedTotalNanos
	if raw.PricedEventCount != raw.EventCount {
		coverage = domain.EstimatedCostCoveragePartial
		var err error
		for _, component := range []struct {
			name  string
			value int64
		}{
			{"input cost", raw.UnpricedKnownInputNanos},
			{"cached input cost", raw.UnpricedKnownCachedInputNanos},
			{"output cost", raw.UnpricedKnownOutputNanos},
		} {
			total, err = checkedUsageAdd(component.name, total, component.value)
			if err != nil {
				return nil, err
			}
		}
		if total == 0 {
			return nil, nil
		}
	}
	providerAttribution, err := estimatedCostProviderAttribution(raw)
	if err != nil {
		return nil, err
	}
	return &domain.EstimatedCost{
		TotalNanos:          total,
		InputNanos:          knownComponent(raw.EventCount, raw.KnownInputCount, raw.KnownInputNanos),
		CachedInputNanos:    knownComponent(raw.EventCount, raw.KnownCachedInputCount, raw.KnownCachedInputNanos),
		OutputNanos:         knownComponent(raw.EventCount, raw.KnownOutputCount, raw.KnownOutputNanos),
		Coverage:            coverage,
		ProviderAttribution: providerAttribution,
	}, nil
}

func estimatedCostProviderAttribution(raw domain.UsageCostAggregate) (domain.EstimatedCostProviderAttribution, error) {
	switch {
	case raw.ObservedCostEventCount > 0 && raw.InferredCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionMixed, nil
	case raw.InferredCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionInferred, nil
	case raw.ObservedCostEventCount > 0:
		return domain.EstimatedCostProviderAttributionObserved, nil
	default:
		return "", fmt.Errorf("usage estimated cost has no provider attribution")
	}
}

func knownComponent(eventCount, knownCount, value int64) *int64 {
	if eventCount == knownCount {
		return &value
	}
	return nil
}

func mergeUsageCostAggregate(dst *domain.UsageCostAggregate, src domain.UsageCostAggregate) error {
	if err := validateUsageCostAggregate(src); err != nil {
		return err
	}
	fields := []struct {
		name string
		dst  *int64
		src  int64
	}{
		{"cost event count", &dst.EventCount, src.EventCount},
		{"priced event count", &dst.PricedEventCount, src.PricedEventCount},
		{"priced total cost", &dst.PricedTotalNanos, src.PricedTotalNanos},
		{"observed cost event count", &dst.ObservedCostEventCount, src.ObservedCostEventCount},
		{"inferred cost event count", &dst.InferredCostEventCount, src.InferredCostEventCount},
		{"known input count", &dst.KnownInputCount, src.KnownInputCount},
		{"known input cost", &dst.KnownInputNanos, src.KnownInputNanos},
		{"unpriced known input cost", &dst.UnpricedKnownInputNanos, src.UnpricedKnownInputNanos},
		{"known cached input count", &dst.KnownCachedInputCount, src.KnownCachedInputCount},
		{"known cached input cost", &dst.KnownCachedInputNanos, src.KnownCachedInputNanos},
		{"unpriced known cached input cost", &dst.UnpricedKnownCachedInputNanos, src.UnpricedKnownCachedInputNanos},
		{"known output count", &dst.KnownOutputCount, src.KnownOutputCount},
		{"known output cost", &dst.KnownOutputNanos, src.KnownOutputNanos},
		{"unpriced known output cost", &dst.UnpricedKnownOutputNanos, src.UnpricedKnownOutputNanos},
	}
	for _, field := range fields {
		value, err := checkedUsageAdd(field.name, *field.dst, field.src)
		if err != nil {
			return err
		}
		*field.dst = value
	}
	return nil
}

func validateUsageCostAggregate(raw domain.UsageCostAggregate) error {
	values := []struct {
		name  string
		value int64
	}{
		{"event count", raw.EventCount}, {"priced event count", raw.PricedEventCount}, {"priced total cost", raw.PricedTotalNanos},
		{"observed cost event count", raw.ObservedCostEventCount}, {"inferred cost event count", raw.InferredCostEventCount},
		{"known input count", raw.KnownInputCount}, {"known input cost", raw.KnownInputNanos}, {"unpriced known input cost", raw.UnpricedKnownInputNanos},
		{"known cached input count", raw.KnownCachedInputCount}, {"known cached input cost", raw.KnownCachedInputNanos}, {"unpriced known cached input cost", raw.UnpricedKnownCachedInputNanos},
		{"known output count", raw.KnownOutputCount}, {"known output cost", raw.KnownOutputNanos}, {"unpriced known output cost", raw.UnpricedKnownOutputNanos},
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("usage %s must be nonnegative", item.name)
		}
	}
	if raw.PricedEventCount > raw.EventCount || raw.KnownInputCount > raw.EventCount ||
		raw.KnownCachedInputCount > raw.EventCount || raw.KnownOutputCount > raw.EventCount ||
		raw.ObservedCostEventCount > raw.EventCount || raw.InferredCostEventCount > raw.EventCount ||
		raw.InferredCostEventCount > raw.EventCount-raw.ObservedCostEventCount {
		return fmt.Errorf("usage cost coverage count exceeds event count")
	}
	if raw.UnpricedKnownInputNanos > raw.KnownInputNanos ||
		raw.UnpricedKnownCachedInputNanos > raw.KnownCachedInputNanos ||
		raw.UnpricedKnownOutputNanos > raw.KnownOutputNanos {
		return fmt.Errorf("usage unpriced component cost exceeds known component cost")
	}
	return nil
}

func checkedUsageAdd(label string, left, right int64) (int64, error) {
	if left < 0 || right < 0 {
		return 0, fmt.Errorf("usage %s must be nonnegative", label)
	}
	if left > math.MaxInt64-right {
		return 0, fmt.Errorf("usage %s overflows int64", label)
	}
	return left + right, nil
}

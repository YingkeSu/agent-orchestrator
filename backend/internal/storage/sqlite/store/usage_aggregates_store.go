package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// AggregateUsageByModel returns per-model usage rollups over an optional
// created_at range with optional source/model filters. source is a usage source
// kind (claude_main, claude_subagent, codex_rollout, kimi_wire); model is an
// exact model id. Empty values leave that filter unbounded.
func (s *Store) AggregateUsageByModel(ctx context.Context, from, to *time.Time, source, model string) ([]domain.UsageModelScopeAggregate, error) {
	rows, err := s.qr.AggregateUsageByModel(ctx, gen.AggregateUsageByModelParams{
		From:   ptrTimeToNullTime(from),
		To:     ptrTimeToNullTime(to),
		Source: stringOrNull(source),
		Model:  stringOrNull(model),
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate usage by model: %w", err)
	}
	out := make([]domain.UsageModelScopeAggregate, 0, len(rows))
	for _, row := range rows {
		// The per-model and per-provider generated rows share one shape; the
		// conversion is safe because the structs are identical.
		view := usageScopeGenViewFromRow(gen.AggregateUsageByProviderRow(row))
		out = append(out, domain.UsageModelScopeAggregate{
			ModelID: view.key, Tokens: view.tokens, Cost: view.cost,
			ObservedEventCount: view.observed, InferredEventCount: view.inferred,
		})
	}
	return out, nil
}

// AggregateUsageByProvider returns per-billing-provider usage rollups over an
// optional created_at range with optional source/model filters. Events with no
// billing provider attribution group under the empty ProviderID. source is a
// usage source kind; model is an exact model id. Empty values leave that filter
// unbounded.
func (s *Store) AggregateUsageByProvider(ctx context.Context, from, to *time.Time, source, model string) ([]domain.UsageProviderScopeAggregate, error) {
	rows, err := s.qr.AggregateUsageByProvider(ctx, gen.AggregateUsageByProviderParams{
		From:   ptrTimeToNullTime(from),
		To:     ptrTimeToNullTime(to),
		Source: stringOrNull(source),
		Model:  stringOrNull(model),
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate usage by provider: %w", err)
	}
	out := make([]domain.UsageProviderScopeAggregate, 0, len(rows))
	for _, row := range rows {
		view := usageScopeGenViewFromRow(row)
		out = append(out, domain.UsageProviderScopeAggregate{
			ProviderID: view.key, Tokens: view.tokens, Cost: view.cost,
			ObservedEventCount: view.observed, InferredEventCount: view.inferred,
		})
	}
	return out, nil
}

type usageScopeGenView struct {
	key      string
	tokens   domain.UsageTokenMetrics
	cost     domain.UsageCostAggregate
	observed int64
	inferred int64
}

// usageScopeGenViewFromRow applies the full-knowledge metric rule to one
// storage rollup row. Both generated rollup rows carry group_key plus the
// summary counters, so one mapper serves the model and provider queries.
func usageScopeGenViewFromRow(row gen.AggregateUsageByProviderRow) usageScopeGenView {
	return usageScopeGenView{
		key: row.GroupKey,
		tokens: scopeTokenMetrics(
			row.EventCount, row.InputTokens, row.KnownInputTokenCount,
			row.CachedInputTokens, row.KnownCachedInputTokenCount,
			row.UncachedInputTokens, row.KnownUncachedInputTokenCount,
			row.OutputTokens, row.KnownOutputTokenCount,
		),
		cost: scopeCostAggregate(
			row.EventCount, row.PricedEventCount, row.PricedTotalNanos,
			row.ObservedCostEventCount, row.InferredCostEventCount,
			row.KnownInputCount, row.KnownInputNanos, row.UnpricedKnownInputNanos,
			row.KnownCachedInputCount, row.KnownCachedInputNanos, row.UnpricedKnownCachedInputNanos,
			row.KnownOutputCount, row.KnownOutputNanos, row.UnpricedKnownOutputNanos,
		),
		observed: row.ObservedEventCount,
		inferred: row.InferredEventCount,
	}
}

// scopeTokenMetrics applies the same full-knowledge rule as the summary
// aggregate: a summed metric is only meaningful when every event in the group
// carried it.
func scopeTokenMetrics(eventCount, inputTokens, knownInputCount, cachedInputTokens, knownCachedInputCount, uncachedInputTokens, knownUncachedInputCount, outputTokens, knownOutputCount int64) domain.UsageTokenMetrics {
	return domain.UsageTokenMetrics{
		InputTokens:         int64PtrWhen(inputTokens, knownInputCount == eventCount),
		CachedInputTokens:   int64PtrWhen(cachedInputTokens, knownCachedInputCount == eventCount),
		UncachedInputTokens: int64PtrWhen(uncachedInputTokens, knownUncachedInputCount == eventCount),
		OutputTokens:        int64PtrWhen(outputTokens, knownOutputCount == eventCount),
	}
}

func scopeCostAggregate(eventCount, pricedEventCount, pricedTotalNanos, observedCostEventCount, inferredCostEventCount, knownInputCount, knownInputNanos, unpricedKnownInputNanos, knownCachedInputCount, knownCachedInputNanos, unpricedKnownCachedInputNanos, knownOutputCount, knownOutputNanos, unpricedKnownOutputNanos int64) domain.UsageCostAggregate {
	return domain.UsageCostAggregate{
		EventCount: eventCount, PricedEventCount: pricedEventCount, PricedTotalNanos: pricedTotalNanos,
		ObservedCostEventCount: observedCostEventCount, InferredCostEventCount: inferredCostEventCount,
		KnownInputCount: knownInputCount, KnownInputNanos: knownInputNanos,
		UnpricedKnownInputNanos: unpricedKnownInputNanos,
		KnownCachedInputCount:   knownCachedInputCount, KnownCachedInputNanos: knownCachedInputNanos,
		UnpricedKnownCachedInputNanos: unpricedKnownCachedInputNanos,
		KnownOutputCount:              knownOutputCount, KnownOutputNanos: knownOutputNanos,
		UnpricedKnownOutputNanos: unpricedKnownOutputNanos,
	}
}

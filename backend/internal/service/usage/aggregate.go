package usage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Models returns per-model usage rollups over the optional created_at range.
// source filters by usage source kind and model by exact model id; empty values
// leave the filter unbounded. Rows are sorted by total cost descending, rows
// without a known cost last, then by model id.
func (r *SummaryReader) Models(ctx context.Context, from, to *time.Time, source, model string) ([]domain.ModelUsageStatsRow, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("usage summary store is unavailable")
	}
	rows, err := r.store.AggregateUsageByModel(ctx, from, to, source, model)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ModelUsageStatsRow, 0, len(rows))
	for _, row := range rows {
		stats, err := usageAggregateStats(row.Tokens, row.Cost)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.ModelUsageStatsRow{ModelID: row.ModelID, Stats: stats})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return usageStatsLess(out[i].ModelID, out[i].Stats.TotalCostNanos, out[j].ModelID, out[j].Stats.TotalCostNanos)
	})
	return out, nil
}

// Providers returns per-billing-provider usage rollups over the optional
// created_at range with the same optional filters as Models. AttributionSource
// reports how the row's billing provider was reached (observed, inferred, or
// mixed) so inferred rows display honestly; it is empty for the unattributed
// bucket. Rows are sorted by total cost descending, rows without a known cost
// last, then by provider id.
func (r *SummaryReader) Providers(ctx context.Context, from, to *time.Time, source, model string) ([]domain.ProviderUsageStatsRow, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("usage summary store is unavailable")
	}
	rows, err := r.store.AggregateUsageByProvider(ctx, from, to, source, model)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ProviderUsageStatsRow, 0, len(rows))
	for _, row := range rows {
		stats, err := usageAggregateStats(row.Tokens, row.Cost)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.ProviderUsageStatsRow{
			ProviderID:        row.ProviderID,
			AttributionSource: usageAttributionSource(row.ObservedEventCount, row.InferredEventCount),
			Stats:             stats,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return usageStatsLess(out[i].ProviderID, out[i].Stats.TotalCostNanos, out[j].ProviderID, out[j].Stats.TotalCostNanos)
	})
	return out, nil
}

// usageAggregateStats derives one table row's metrics from the raw storage
// aggregate. Processed tokens stay nil unless every event in the row reported
// both input and output; the cost mirrors the scope estimate, so a partial
// coverage row carries the same estimated lower bound as the summary.
func usageAggregateStats(tokens domain.UsageTokenMetrics, cost domain.UsageCostAggregate) (domain.UsageAggregateStats, error) {
	var processed *int64
	if tokens.InputTokens != nil && tokens.OutputTokens != nil {
		processedValue := *tokens.InputTokens + *tokens.OutputTokens
		processed = &processedValue
	}
	estimate, err := estimatedCost(cost)
	if err != nil {
		return domain.UsageAggregateStats{}, err
	}
	var total *int64
	var average *float64
	if estimate != nil {
		total = &estimate.TotalNanos
		if cost.EventCount > 0 {
			avg := float64(estimate.TotalNanos) / float64(cost.EventCount)
			average = &avg
		}
	}
	return domain.UsageAggregateStats{
		RequestCount: cost.EventCount, ProcessedTokens: processed,
		TotalCostNanos: total, AverageCostPerRequestNanos: average,
	}, nil
}

// usageAttributionSource names how the billing provider of a row's events was
// reached, or the empty string when no event in the row carried attribution.
func usageAttributionSource(observed, inferred int64) domain.EstimatedCostProviderAttribution {
	switch {
	case observed > 0 && inferred > 0:
		return domain.EstimatedCostProviderAttributionMixed
	case inferred > 0:
		return domain.EstimatedCostProviderAttributionInferred
	case observed > 0:
		return domain.EstimatedCostProviderAttributionObserved
	default:
		return ""
	}
}

// usageStatsLess orders aggregate table rows by total cost descending with
// unknown costs last, then by the group key.
func usageStatsLess(nameA string, costA *int64, nameB string, costB *int64) bool {
	if (costA == nil) != (costB == nil) {
		return costA != nil
	}
	if costA != nil && *costA != *costB {
		return *costA > *costB
	}
	return nameA < nameB
}

package domain

// UsageModelScopeAggregate is one storage-level per-model rollup row read
// before the service applies user-facing coverage rules.
type UsageModelScopeAggregate struct {
	ModelID            string
	Tokens             UsageTokenMetrics
	Cost               UsageCostAggregate
	ObservedEventCount int64
	InferredEventCount int64
}

// UsageProviderScopeAggregate is one storage-level per-billing-provider rollup
// row. ProviderID is empty for events with no billing provider attribution.
type UsageProviderScopeAggregate struct {
	ProviderID         string
	Tokens             UsageTokenMetrics
	Cost               UsageCostAggregate
	ObservedEventCount int64
	InferredEventCount int64
}

// UsageAggregateStats is the derived metric block shared by every row of the
// usage statistics aggregate tables.
type UsageAggregateStats struct {
	RequestCount               int64
	ProcessedTokens            *int64
	TotalCostNanos             *int64
	AverageCostPerRequestNanos *float64
}

// ModelUsageStatsRow is one per-model row of the usage statistics tables.
type ModelUsageStatsRow struct {
	ModelID string
	Stats   UsageAggregateStats
}

// ProviderUsageStatsRow is one per-billing-provider row of the usage statistics
// tables. AttributionSource is empty when the row carries no billing
// attribution (the unattributed bucket).
type ProviderUsageStatsRow struct {
	ProviderID        string
	AttributionSource EstimatedCostProviderAttribution
	Stats             UsageAggregateStats
}

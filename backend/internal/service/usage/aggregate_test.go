package usage

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func (s *usageSummaryStoreStub) AggregateUsageByModel(_ context.Context, from, to *time.Time, source, model string) ([]domain.UsageModelScopeAggregate, error) {
	s.modelFrom, s.modelTo, s.modelSource, s.modelModel = from, to, source, model
	return s.modelRows, nil
}

func (s *usageSummaryStoreStub) AggregateUsageByProvider(_ context.Context, from, to *time.Time, source, model string) ([]domain.UsageProviderScopeAggregate, error) {
	s.providerFrom, s.providerTo, s.providerSource, s.providerModel = from, to, source, model
	return s.providerRows, nil
}

func TestSummaryReaderModelsDerivesAndSortsStats(t *testing.T) {
	store := &usageSummaryStoreStub{modelRows: []domain.UsageModelScopeAggregate{
		{
			ModelID:            "claude-x",
			Tokens:             domain.UsageTokenMetrics{InputTokens: int64Ptr(70), OutputTokens: int64Ptr(15)},
			Cost:               completeCostAggregate(2, 30, 20, 5, 5),
			ObservedEventCount: 1, InferredEventCount: 1,
		},
		{
			ModelID: "unknown-cost",
			Tokens:  domain.UsageTokenMetrics{InputTokens: int64Ptr(10), OutputTokens: int64Ptr(2)},
			Cost:    domain.UsageCostAggregate{EventCount: 1},
		},
		{
			ModelID: "gpt-5",
			Tokens:  domain.UsageTokenMetrics{InputTokens: int64Ptr(150), OutputTokens: int64Ptr(50)},
			Cost:    completeCostAggregate(2, 210, 130, 25, 55),
		},
	}}
	reader := NewSummaryReader(store)

	got, err := reader.Models(context.Background(), nil, nil, "claude_main", "")
	mustNoError(t, err)
	if store.modelSource != "claude_main" || store.modelModel != "" {
		t.Fatalf("filters = source %q model %q", store.modelSource, store.modelModel)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	assertModelStatsRow(t, got[0], "gpt-5", 2, 200, 210, 105)
	assertModelStatsRow(t, got[1], "claude-x", 2, 85, 30, 15)
	if got[2].ModelID != "unknown-cost" || got[2].Stats.RequestCount != 1 ||
		got[2].Stats.ProcessedTokens == nil || *got[2].Stats.ProcessedTokens != 12 ||
		got[2].Stats.TotalCostNanos != nil || got[2].Stats.AverageCostPerRequestNanos != nil {
		t.Fatalf("unknown cost row = %+v", got[2])
	}
}

func TestSummaryReaderModelsForwardRangeAndModelFilter(t *testing.T) {
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{modelRows: []domain.UsageModelScopeAggregate{
		{ModelID: "gpt-5", Tokens: testUsageMetrics(1, 0, 1, 1), Cost: completeCostAggregate(1, 10, 5, 0, 5)},
	}}
	reader := NewSummaryReader(store)

	got, err := reader.Models(context.Background(), &from, &to, "", "gpt-5")
	mustNoError(t, err)
	if len(got) != 1 || got[0].ModelID != "gpt-5" {
		t.Fatalf("rows = %+v", got)
	}
	if store.modelFrom == nil || !store.modelFrom.Equal(from) || store.modelTo == nil || !store.modelTo.Equal(to) ||
		store.modelModel != "gpt-5" {
		t.Fatalf("range/filter = %v .. %v model %q", store.modelFrom, store.modelTo, store.modelModel)
	}
}

func TestSummaryReaderModelsEmptyRange(t *testing.T) {
	store := &usageSummaryStoreStub{}
	got, err := NewSummaryReader(store).Models(context.Background(), nil, nil, "", "")
	mustNoError(t, err)
	if len(got) != 0 {
		t.Fatalf("rows = %+v, want empty", got)
	}
}

func TestSummaryReaderProvidersExposeAttributionAndSort(t *testing.T) {
	store := &usageSummaryStoreStub{providerRows: []domain.UsageProviderScopeAggregate{
		{
			ProviderID: "openai", Tokens: testUsageMetrics(150, 50, 100, 50),
			Cost:               completeCostAggregate(2, 210, 130, 25, 55),
			ObservedEventCount: 1, InferredEventCount: 1,
		},
		{
			ProviderID: "anthropic", Tokens: testUsageMetrics(70, 40, 30, 15),
			Cost:               completeCostAggregate(1, 30, 20, 5, 5),
			ObservedEventCount: 1, InferredEventCount: 0,
		},
		{
			ProviderID: "inferred-only", Tokens: testUsageMetrics(10, 0, 10, 5),
			Cost:               completeCostAggregate(1, 7, 5, 0, 2),
			ObservedEventCount: 0, InferredEventCount: 1,
		},
		{
			ProviderID: "", Tokens: testUsageMetrics(5, 0, 5, 2),
			Cost:               domain.UsageCostAggregate{EventCount: 1},
			ObservedEventCount: 0, InferredEventCount: 0,
		},
	}}
	reader := NewSummaryReader(store)

	got, err := reader.Providers(context.Background(), nil, nil, "", "")
	mustNoError(t, err)
	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4", len(got))
	}
	if got[0].ProviderID != "openai" || got[0].AttributionSource != domain.EstimatedCostProviderAttributionMixed ||
		got[0].Stats.TotalCostNanos == nil || *got[0].Stats.TotalCostNanos != 210 ||
		got[0].Stats.AverageCostPerRequestNanos == nil || *got[0].Stats.AverageCostPerRequestNanos != 105 {
		t.Fatalf("openai row = %+v", got[0])
	}
	if got[1].ProviderID != "anthropic" || got[1].AttributionSource != domain.EstimatedCostProviderAttributionObserved {
		t.Fatalf("anthropic row = %+v", got[1])
	}
	if got[2].ProviderID != "inferred-only" || got[2].AttributionSource != domain.EstimatedCostProviderAttributionInferred {
		t.Fatalf("inferred row = %+v", got[2])
	}
	if got[3].ProviderID != "" || got[3].AttributionSource != "" ||
		got[3].Stats.ProcessedTokens == nil || *got[3].Stats.ProcessedTokens != 7 ||
		got[3].Stats.TotalCostNanos != nil {
		t.Fatalf("unattributed row = %+v", got[3])
	}
}

func TestSummaryReaderModelsDropsPartiallyKnownTokens(t *testing.T) {
	store := &usageSummaryStoreStub{modelRows: []domain.UsageModelScopeAggregate{
		{
			ModelID: "partial-tokens",
			Tokens: domain.UsageTokenMetrics{
				InputTokens: int64Ptr(100), CachedInputTokens: int64Ptr(40),
				UncachedInputTokens: int64Ptr(60), OutputTokens: nil,
			},
			Cost: completeCostAggregate(2, 40, 20, 10, 10),
		},
	}}
	got, err := NewSummaryReader(store).Models(context.Background(), nil, nil, "", "")
	mustNoError(t, err)
	if len(got) != 1 || got[0].Stats.ProcessedTokens != nil {
		t.Fatalf("partial tokens row = %+v, want nil processed", got)
	}
}

func assertModelStatsRow(t *testing.T, row domain.ModelUsageStatsRow, model string, requests, tokens, total int64, average float64) {
	t.Helper()
	if row.ModelID != model || row.Stats.RequestCount != requests {
		t.Fatalf("row = %+v, want %q with %d requests", row, model, requests)
	}
	if row.Stats.ProcessedTokens == nil || *row.Stats.ProcessedTokens != tokens {
		t.Fatalf("row %q tokens = %v, want %d", model, row.Stats.ProcessedTokens, tokens)
	}
	if row.Stats.TotalCostNanos == nil || *row.Stats.TotalCostNanos != total {
		t.Fatalf("row %q cost = %v, want %d", model, row.Stats.TotalCostNanos, total)
	}
	if row.Stats.AverageCostPerRequestNanos == nil || *row.Stats.AverageCostPerRequestNanos != average {
		t.Fatalf("row %q average = %v, want %v", model, row.Stats.AverageCostPerRequestNanos, average)
	}
}

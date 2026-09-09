package usage

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type usageSummaryStoreStub struct {
	projectID    domain.ProjectID
	rows         []domain.CompactSessionUsageAggregate
	session      domain.SessionRecord
	found        bool
	incomplete   bool
	models       []domain.UsageModelAggregate
	global       domain.GlobalUsageAggregate
	dims         domain.UsageSummaryDimensions
	globalFrom   *time.Time
	globalTo     *time.Time
	globalSource string
	globalModel  string
	trend        []domain.UsageTrendBucket
	trendFrom    *time.Time
	trendTo      *time.Time
	trendSecs    int64
	trendSrc     string
	trendModel   string
	calls        [6]int

	modelRows      []domain.UsageModelScopeAggregate
	providerRows   []domain.UsageProviderScopeAggregate
	modelFrom      *time.Time
	modelTo        *time.Time
	modelSource    string
	modelModel     string
	providerFrom   *time.Time
	providerTo     *time.Time
	providerSource string
	providerModel  string

	logRows   []domain.UsageRequestLogEntry
	logFrom   *time.Time
	logTo     *time.Time
	logSource string
	logModel  string
	logBefore *int64
	logLimit  int64

	runtimeAgg  domain.SessionRuntimeTimingAggregate
	chatTurns   []domain.ConversationRuntimeTurnFact
	runtimeCall int
}

func (s *usageSummaryStoreStub) ListCompactSessionUsageAggregates(_ context.Context, id domain.ProjectID) ([]domain.CompactSessionUsageAggregate, error) {
	s.projectID, s.calls[0] = id, s.calls[0]+1
	return s.rows, nil
}
func (s *usageSummaryStoreStub) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	s.calls[1]++
	return s.session, s.found, nil
}
func (s *usageSummaryStoreStub) ListUsageModelAggregates(context.Context, domain.SessionID) ([]domain.UsageModelAggregate, error) {
	s.calls[2]++
	return s.models, nil
}
func (s *usageSummaryStoreStub) GetUsageSessionIncomplete(context.Context, domain.SessionID) (bool, error) {
	s.calls[3]++
	return s.incomplete, nil
}
func (s *usageSummaryStoreStub) AggregateUsageSummary(_ context.Context, from, to *time.Time, source, model string) (domain.GlobalUsageAggregate, error) {
	s.calls[4]++
	s.globalFrom, s.globalTo, s.globalSource, s.globalModel = from, to, source, model
	return s.global, nil
}
func (s *usageSummaryStoreStub) ListUsageSummaryDimensions(_ context.Context, from, to *time.Time, source, model string) (domain.UsageSummaryDimensions, error) {
	s.calls[5]++
	return s.dims, nil
}
func (s *usageSummaryStoreStub) ListUsageRequestLog(_ context.Context, from, to *time.Time, source, model string, beforeID *int64, limit int64) ([]domain.UsageRequestLogEntry, error) {
	s.logFrom, s.logTo, s.logSource, s.logModel, s.logBefore, s.logLimit = from, to, source, model, beforeID, limit
	return s.logRows, nil
}
func (s *usageSummaryStoreStub) AggregateUsageTrend(_ context.Context, from, to *time.Time, seconds int64, source, model string) ([]domain.UsageTrendBucket, error) {
	s.calls[5]++
	s.trendFrom, s.trendTo, s.trendSecs, s.trendSrc, s.trendModel = from, to, seconds, source, model
	return s.trend, nil
}
func (s *usageSummaryStoreStub) AggregateSessionRuntimeTiming(context.Context, domain.SessionID) (domain.SessionRuntimeTimingAggregate, error) {
	s.runtimeCall++
	return s.runtimeAgg, nil
}
func (s *usageSummaryStoreStub) ListConversationRuntimeTurnFacts(context.Context, domain.SessionID) ([]domain.ConversationRuntimeTurnFact, error) {
	s.runtimeCall++
	return s.chatTurns, nil
}

func TestSummaryReaderListCompactUsesOneBatchRead(t *testing.T) {
	zeroProcessed, partialProcessed := int64(0), int64(120)
	store := &usageSummaryStoreStub{rows: []domain.CompactSessionUsageAggregate{
		{
			SessionID: "zero", ProcessedTokens: &zeroProcessed,
			Cost: completeCostAggregate(1, 0, 0, 0, 0),
		},
		{
			SessionID: "partial", ProcessedTokens: &partialProcessed, Incomplete: true,
			Cost: domain.UsageCostAggregate{
				EventCount:                    1,
				ObservedCostEventCount:        1,
				KnownInputCount:               1,
				KnownInputNanos:               30,
				UnpricedKnownInputNanos:       30,
				KnownCachedInputCount:         1,
				KnownCachedInputNanos:         0,
				UnpricedKnownCachedInputNanos: 0,
				KnownOutputCount:              1,
				KnownOutputNanos:              5,
				UnpricedKnownOutputNanos:      5,
			},
		},
	}}

	got, err := NewSummaryReader(store).ListCompact(context.Background(), "reverb")
	mustNoError(t, err)
	if store.calls[0] != 1 || store.projectID != "reverb" || len(got) != 2 {
		t.Fatalf("read=%d project=%q items=%+v", store.calls[0], store.projectID, got)
	}
	if got[0].EstimatedCost == nil || got[0].EstimatedCost.Coverage != domain.EstimatedCostCoverageComplete || got[0].EstimatedCost.TotalNanos != 0 {
		t.Fatalf("zero cost = %+v, want complete zero", got[0].EstimatedCost)
	}
	if got[1].ProcessedTokens == nil || *got[1].ProcessedTokens != 120 || !got[1].Incomplete ||
		got[1].EstimatedCost == nil ||
		got[1].EstimatedCost.Coverage != domain.EstimatedCostCoveragePartial || got[1].EstimatedCost.TotalNanos != 35 {
		t.Fatalf("partial compact summary = %+v", got[1])
	}
	if got[1].EstimatedCost.InputNanos == nil || *got[1].EstimatedCost.InputNanos != 30 ||
		got[1].EstimatedCost.CachedInputNanos == nil || *got[1].EstimatedCost.CachedInputNanos != 0 {
		t.Fatalf("partial components = %+v", got[1].EstimatedCost)
	}
}

func TestSummaryReaderGetPreservesStrongestPartialLowerBoundWithoutDoubleCounting(t *testing.T) {
	store := &usageSummaryStoreStub{
		found:      true,
		incomplete: true,
		session:    domain.SessionRecord{ID: "reverb-12", Harness: domain.HarnessCodex},
		models: []domain.UsageModelAggregate{
			{
				Harness: domain.HarnessClaudeCode, ModelID: "<synthetic>",
				Tokens: testUsageMetrics(0, 0, 0, 0),
			},
			{
				Harness: domain.HarnessCodex, ModelID: "gpt-5.6",
				Tokens: testUsageMetrics(1000, 400, 600, 200),
				Cost:   completeCostAggregate(1, 100, 20, 10, 70),
			},
			{
				Harness: domain.HarnessClaudeCode, ModelID: "claude-sonnet",
				Tokens: testUsageMetrics(100, 20, 80, 25),
				Cost: domain.UsageCostAggregate{
					EventCount:               1,
					ObservedCostEventCount:   1,
					KnownInputCount:          1,
					KnownInputNanos:          30,
					UnpricedKnownInputNanos:  30,
					KnownCachedInputCount:    1,
					KnownCachedInputNanos:    0,
					KnownOutputCount:         1,
					KnownOutputNanos:         5,
					UnpricedKnownOutputNanos: 5,
				},
			},
		},
	}

	got, err := NewSummaryReader(store).Get(context.Background(), "reverb-12")
	mustNoError(t, err)
	if !got.Incomplete {
		t.Fatal("token integrity failure did not remain independent from cost coverage")
	}
	if got.Totals.InputTokens == nil || *got.Totals.InputTokens != 1100 ||
		got.Totals.OutputTokens == nil || *got.Totals.OutputTokens != 225 ||
		got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 1325 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	cost := got.Totals.EstimatedCost
	if cost == nil || cost.Coverage != domain.EstimatedCostCoveragePartial || cost.TotalNanos != 135 {
		t.Fatalf("session cost = %+v, want partial lower bound 100+30+5", cost)
	}
	if cost.InputNanos == nil || *cost.InputNanos != 50 ||
		cost.CachedInputNanos == nil || *cost.CachedInputNanos != 10 ||
		cost.OutputNanos == nil || *cost.OutputNanos != 75 {
		t.Fatalf("session component coverage = %+v", cost)
	}
	if len(got.Harnesses) != 2 || len(got.Harnesses[0].Models) != 1 || len(got.Harnesses[1].Models) != 1 ||
		got.Harnesses[0].Models[0].ModelID != "gpt-5.6" ||
		got.Harnesses[1].Models[0].ModelID != "claude-sonnet" {
		t.Fatalf("model grouping = %+v", got.Harnesses)
	}
	if got.Harnesses[0].Models[0].Totals.EstimatedCost == nil ||
		got.Harnesses[0].Models[0].Totals.EstimatedCost.Coverage != domain.EstimatedCostCoverageComplete ||
		got.Harnesses[1].Models[0].Totals.EstimatedCost == nil ||
		got.Harnesses[1].Models[0].Totals.EstimatedCost.TotalNanos != 35 {
		t.Fatalf("model costs = %+v", got.Harnesses)
	}
	for _, harness := range got.Harnesses {
		for _, model := range harness.Models {
			if model.ModelID == "<synthetic>" {
				t.Fatalf("synthetic model leaked into summary: %+v", got.Harnesses)
			}
		}
	}
	if got.Harnesses[0].Totals.ProcessedTokens == nil || *got.Harnesses[0].Totals.ProcessedTokens != 1200 ||
		got.Harnesses[0].Models[0].Totals.ProcessedTokens == nil || *got.Harnesses[0].Models[0].Totals.ProcessedTokens != 1200 ||
		got.Harnesses[1].Totals.ProcessedTokens == nil || *got.Harnesses[1].Totals.ProcessedTokens != 125 {
		t.Fatalf("processed totals by scope = %+v", got.Harnesses)
	}
	if store.calls != [6]int{0, 1, 1, 1, 0, 0} {
		t.Fatalf("store calls = %v", store.calls)
	}
}

// Break caught: inferred prices were aggregated into the same dollar value as
// observed prices without preserving the distinction the UI needs to explain
// that the billing provider has not been confirmed.
func TestSummaryReaderReportsCostProviderAttributionAtEveryScope(t *testing.T) {
	observed := completeCostAggregate(1, 100, 20, 10, 70)
	inferred := completeCostAggregate(1, 200, 40, 20, 140)
	inferred.ObservedCostEventCount = 0
	inferred.InferredCostEventCount = 1
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "reverb-12", Harness: domain.HarnessClaudeCode},
		models: []domain.UsageModelAggregate{
			{Harness: domain.HarnessClaudeCode, ModelID: "claude-observed", Tokens: testUsageMetrics(1, 0, 1, 1), Cost: observed},
			{Harness: domain.HarnessClaudeCode, ModelID: "claude-inferred", Tokens: testUsageMetrics(1, 0, 1, 1), Cost: inferred},
		},
	}

	got, err := NewSummaryReader(store).Get(context.Background(), "reverb-12")
	mustNoError(t, err)
	if got.Totals.EstimatedCost == nil ||
		got.Totals.EstimatedCost.ProviderAttribution != domain.EstimatedCostProviderAttributionMixed {
		t.Fatalf("session attribution = %+v, want mixed", got.Totals.EstimatedCost)
	}
	if len(got.Harnesses) != 1 || got.Harnesses[0].Totals.EstimatedCost == nil ||
		got.Harnesses[0].Totals.EstimatedCost.ProviderAttribution != domain.EstimatedCostProviderAttributionMixed {
		t.Fatalf("harness attribution = %+v, want mixed", got.Harnesses)
	}
	models := got.Harnesses[0].Models
	if len(models) != 2 || models[0].Totals.EstimatedCost == nil || models[1].Totals.EstimatedCost == nil ||
		models[0].Totals.EstimatedCost.ProviderAttribution != domain.EstimatedCostProviderAttributionObserved ||
		models[1].Totals.EstimatedCost.ProviderAttribution != domain.EstimatedCostProviderAttributionInferred {
		t.Fatalf("model attributions = %+v, want observed then inferred", models)
	}
}

func TestSummaryReaderReturnsUnavailableCostForZeroPartialLowerBound(t *testing.T) {
	store := &usageSummaryStoreStub{rows: []domain.CompactSessionUsageAggregate{{
		SessionID: "unknown", Cost: domain.UsageCostAggregate{EventCount: 1},
	}}}
	got, err := NewSummaryReader(store).ListCompact(context.Background(), "")
	mustNoError(t, err)
	if len(got) != 1 || got[0].EstimatedCost != nil {
		t.Fatalf("cost = %+v, want unavailable", got)
	}
}

func TestSummaryReaderRejectsAggregateOverflow(t *testing.T) {
	t.Run("partial lower bound", func(t *testing.T) {
		store := &usageSummaryStoreStub{rows: []domain.CompactSessionUsageAggregate{{
			SessionID: "overflow",
			Cost: domain.UsageCostAggregate{
				EventCount:              2,
				PricedEventCount:        1,
				PricedTotalNanos:        math.MaxInt64,
				KnownInputCount:         1,
				KnownInputNanos:         1,
				UnpricedKnownInputNanos: 1,
			},
		}}}
		if _, err := NewSummaryReader(store).ListCompact(context.Background(), ""); err == nil {
			t.Fatal("partial cost overflow returned nil error")
		}
	})

	t.Run("detail cost groups", func(t *testing.T) {
		store := &usageSummaryStoreStub{found: true, models: []domain.UsageModelAggregate{
			{
				Harness: domain.HarnessCodex, ModelID: "one",
				Tokens: testUsageMetrics(1, 0, 1, 0),
				Cost:   completeCostAggregate(1, math.MaxInt64, 0, 0, 0),
			},
			{
				Harness: domain.HarnessCodex, ModelID: "two",
				Tokens: testUsageMetrics(1, 0, 1, 0),
				Cost:   completeCostAggregate(1, 1, 0, 0, 0),
			},
		}}
		if _, err := NewSummaryReader(store).Get(context.Background(), "overflow"); err == nil {
			t.Fatal("detailed cost overflow returned nil error")
		}
	})
}

func TestSummaryReaderGlobalHappyPath(t *testing.T) {
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{global: domain.GlobalUsageAggregate{
		EventCount: 2,
		Tokens:     testUsageMetrics(1100, 400, 700, 200),
		Cost:       completeCostAggregate(2, 300, 100, 40, 160),
	}}

	got, err := NewSummaryReader(store).Global(context.Background(), &from, &to, "", "")
	mustNoError(t, err)
	if store.globalFrom == nil || !store.globalFrom.Equal(from) || store.globalTo == nil || !store.globalTo.Equal(to) {
		t.Fatalf("range bounds = %v .. %v, want %v .. %v", store.globalFrom, store.globalTo, from, to)
	}
	if got.RequestCount != 2 {
		t.Fatalf("request count = %d, want 2", got.RequestCount)
	}
	if got.Totals.InputTokens == nil || *got.Totals.InputTokens != 1100 ||
		got.Totals.CachedInputTokens == nil || *got.Totals.CachedInputTokens != 400 ||
		got.Totals.UncachedInputTokens == nil || *got.Totals.UncachedInputTokens != 700 ||
		got.Totals.OutputTokens == nil || *got.Totals.OutputTokens != 200 ||
		got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 1300 {
		t.Fatalf("global totals = %+v", got.Totals)
	}
	if got.Totals.EstimatedCost == nil || got.Totals.EstimatedCost.TotalNanos != 300 ||
		got.Totals.EstimatedCost.Coverage != domain.EstimatedCostCoverageComplete {
		t.Fatalf("global cost = %+v", got.Totals.EstimatedCost)
	}
	if got.CacheHitRate == nil || *got.CacheHitRate != 400.0/1100.0 {
		t.Fatalf("cache hit rate = %v, want 400/1100", got.CacheHitRate)
	}
}

func TestSummaryReaderGlobalExcludesUnknownMetrics(t *testing.T) {
	store := &usageSummaryStoreStub{global: domain.GlobalUsageAggregate{
		EventCount: 1,
		// Cached input is unknown: the summed uncached/cached split is dropped.
		Tokens: domain.UsageTokenMetrics{
			InputTokens:         int64Ptr(100),
			OutputTokens:        int64Ptr(50),
			CachedInputTokens:   nil,
			UncachedInputTokens: int64Ptr(100),
		},
		Cost: completeCostAggregate(1, 0, 0, 0, 0),
	}}

	got, err := NewSummaryReader(store).Global(context.Background(), nil, nil, "", "")
	mustNoError(t, err)
	if got.Totals.CachedInputTokens != nil {
		t.Fatalf("unknown cached input = %v, want nil", got.Totals.CachedInputTokens)
	}
	if got.CacheHitRate != nil {
		t.Fatalf("cache hit rate with unknown component = %v, want nil", got.CacheHitRate)
	}
	// Known components still aggregate.
	if got.Totals.InputTokens == nil || *got.Totals.InputTokens != 100 ||
		got.Totals.OutputTokens == nil || *got.Totals.OutputTokens != 50 {
		t.Fatalf("known totals = %+v", got.Totals)
	}
}

func TestSummaryReaderGlobalEmptyRange(t *testing.T) {
	store := &usageSummaryStoreStub{global: domain.GlobalUsageAggregate{EventCount: 0}}
	got, err := NewSummaryReader(store).Global(context.Background(), nil, nil, "", "")
	mustNoError(t, err)
	if got.RequestCount != 0 {
		t.Fatalf("request count = %d, want 0", got.RequestCount)
	}
	if got.Totals.InputTokens != nil || got.Totals.OutputTokens != nil ||
		got.Totals.ProcessedTokens != nil || got.Totals.EstimatedCost != nil {
		t.Fatalf("empty range totals = %+v, want all nil", got.Totals)
	}
	if got.CacheHitRate != nil {
		t.Fatalf("empty range cache hit rate = %v, want nil", got.CacheHitRate)
	}
}

func TestSummaryReaderGlobalPassesFiltersAndDimensionsThrough(t *testing.T) {
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{
		global: domain.GlobalUsageAggregate{
			EventCount: 1,
			Tokens:     testUsageMetrics(100, 40, 60, 20),
			Cost:       completeCostAggregate(1, 30, 10, 5, 15),
		},
		dims: domain.UsageSummaryDimensions{
			Sources: []domain.UsageSourceKind{domain.UsageSourceCodexRollout},
			Models:  []string{"gpt-5.6"},
		},
	}

	got, err := NewSummaryReader(store).Global(context.Background(), &from, &to, "codex_rollout", "gpt-5.6")
	mustNoError(t, err)
	if store.calls[4] != 1 || store.calls[5] != 1 ||
		store.globalFrom == nil || !store.globalFrom.Equal(from) ||
		store.globalTo == nil || !store.globalTo.Equal(to) ||
		store.globalSource != "codex_rollout" || store.globalModel != "gpt-5.6" {
		t.Fatalf("filtered aggregate read = from:%v to:%v source:%q model:%q calls:%v",
			store.globalFrom, store.globalTo, store.globalSource, store.globalModel, store.calls)
	}
	if got.RequestCount != 1 || got.Totals.InputTokens == nil || *got.Totals.InputTokens != 100 {
		t.Fatalf("filtered summary = %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0] != domain.UsageSourceCodexRollout ||
		len(got.Models) != 1 || got.Models[0] != "gpt-5.6" {
		t.Fatalf("dimensions = sources:%v models:%v, want codex_rollout and gpt-5.6", got.Sources, got.Models)
	}
}

func TestSummaryReaderGlobalUnknownFilterYieldsEmptyNotError(t *testing.T) {
	store := &usageSummaryStoreStub{global: domain.GlobalUsageAggregate{EventCount: 0}}
	got, err := NewSummaryReader(store).Global(context.Background(), nil, nil, "no_such_source", "no-such-model")
	mustNoError(t, err)
	if got.RequestCount != 0 {
		t.Fatalf("request count = %d, want 0 for an unknown filter", got.RequestCount)
	}
	if got.Totals.InputTokens != nil || got.Totals.OutputTokens != nil || got.Totals.EstimatedCost != nil {
		t.Fatalf("unknown filter totals = %+v, want all nil", got.Totals)
	}
	if len(got.Sources) != 0 || len(got.Models) != 0 {
		t.Fatalf("unknown filter dimensions = %+v, want empty", got)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func testUsageMetrics(input, cachedInput, uncachedInput, output int64) domain.UsageTokenMetrics {
	return domain.UsageTokenMetrics{
		InputTokens: &input, CachedInputTokens: &cachedInput, UncachedInputTokens: &uncachedInput,
		OutputTokens: &output,
	}
}

func TestSummaryReaderGetReturnsUnavailableMetricsWithoutEvents(t *testing.T) {
	store := &usageSummaryStoreStub{found: true, session: domain.SessionRecord{ID: "empty"}}
	got, err := NewSummaryReader(store).Get(context.Background(), "empty")
	mustNoError(t, err)
	if got.Totals.InputTokens != nil || got.Totals.OutputTokens != nil ||
		got.Totals.ProcessedTokens != nil || got.Totals.EstimatedCost != nil || len(got.Harnesses) != 0 {
		t.Fatalf("empty usage = %+v", got)
	}
}

func completeCostAggregate(events, total, input, cachedInput, output int64) domain.UsageCostAggregate {
	return domain.UsageCostAggregate{
		EventCount: events, PricedEventCount: events, PricedTotalNanos: total,
		ObservedCostEventCount: events,
		KnownInputCount:        events, KnownInputNanos: input,
		KnownCachedInputCount: events, KnownCachedInputNanos: cachedInput,
		KnownOutputCount: events, KnownOutputNanos: output,
	}
}

func TestSummaryReaderListRequestLogHappyPath(t *testing.T) {
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	before := int64(99)
	input, cached, output, cost := int64(1100), int64(400), int64(200), int64(135)
	store := &usageSummaryStoreStub{logRows: []domain.UsageRequestLogEntry{
		{
			ID: 5, CreatedAt: &to, BillingProviderID: "anthropic", ModelID: "claude-sonnet",
			InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output,
			EstimatedCostNanos: &cost, SourceKind: domain.UsageSourceClaudeMain,
			SessionID: "reverb-12", SessionExists: true,
		},
	}}

	page, err := NewSummaryReader(store).ListRequestLog(context.Background(), &from, &to, "codex_rollout", "gpt-5", &before, 20)
	mustNoError(t, err)
	if store.logFrom == nil || !store.logFrom.Equal(from) || store.logTo == nil || !store.logTo.Equal(to) ||
		store.logSource != "codex_rollout" || store.logModel != "gpt-5" ||
		store.logBefore == nil || *store.logBefore != 99 || store.logLimit != 20 {
		t.Fatalf("log params = from %v to %v source %q model %q before %v limit %d", store.logFrom, store.logTo, store.logSource, store.logModel, store.logBefore, store.logLimit)
	}
	if len(page.Items) != 1 || page.Items[0].SessionExists != true || page.NextBeforeID != nil {
		t.Fatalf("page = %+v", page)
	}
}

func TestSummaryReaderListRequestLogSetsNextCursorWhenPageIsFull(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{logRows: []domain.UsageRequestLogEntry{
		{ID: 3, CreatedAt: &now, ModelID: "a", SessionID: "s-1"},
		{ID: 2, CreatedAt: &now, ModelID: "b", SessionID: "s-2"},
		{ID: 1, CreatedAt: &now, ModelID: "c", SessionID: "s-3"},
	}}

	page, err := NewSummaryReader(store).ListRequestLog(context.Background(), nil, nil, "", "", nil, 2)
	mustNoError(t, err)
	if store.logLimit != 2 {
		t.Fatalf("store limit = %d, want 2", store.logLimit)
	}
	if len(page.Items) != 2 || page.Items[0].ID != 3 || page.Items[1].ID != 2 {
		t.Fatalf("items = %+v", page.Items)
	}
	if page.NextBeforeID == nil || *page.NextBeforeID != 2 {
		t.Fatalf("nextBeforeId = %v, want 2", page.NextBeforeID)
	}
}

func TestSummaryReaderListRequestLogClampsLimit(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{logRows: []domain.UsageRequestLogEntry{{ID: 1, CreatedAt: &now, SessionID: "s-1"}}}

	if _, err := NewSummaryReader(store).ListRequestLog(context.Background(), nil, nil, "", "", nil, 0); err != nil {
		t.Fatalf("zero limit: %v", err)
	}
	if store.logLimit != 1 {
		t.Fatalf("zero limit clamped to %d, want 1", store.logLimit)
	}
	if _, err := NewSummaryReader(store).ListRequestLog(context.Background(), nil, nil, "", "", nil, 1000); err != nil {
		t.Fatalf("oversize limit: %v", err)
	}
	if store.logLimit != maxRequestLogPageSize {
		t.Fatalf("oversize limit clamped to %d, want %d", store.logLimit, maxRequestLogPageSize)
	}
}

func TestSummaryReaderRuntimeStatsNativeFullData(t *testing.T) {
	llm, tool, firstSum, rateTokens, rateLLM := int64(1_217_000), int64(503_000), int64(48_000), int64(1_680), int64(20_000)
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "reverb-12", Mode: domain.SessionModeTUI},
		models: []domain.UsageModelAggregate{{
			Harness: domain.HarnessCodex, ModelID: "gpt-5.6",
			Tokens: testUsageMetrics(3_000_000, 2_910_000, 90_000, 3_300_000),
			Cost:   completeCostAggregate(15, 300, 100, 50, 150),
		}},
		runtimeAgg: domain.SessionRuntimeTimingAggregate{
			StepCount: 15, TimingRowCount: 15, RoundCount: 11,
			LLMMSTotal: &llm, ToolMSTotal: &tool,
			FirstTokenMSSum: &firstSum, FirstTokenKnown: 12,
			RateOutputTokens: &rateTokens, RateLLMMS: &rateLLM,
		},
	}

	got, err := NewSummaryReader(store).RuntimeStats(context.Background(), "reverb-12")
	mustNoError(t, err)
	if got.Rounds == nil || *got.Rounds != 11 || got.Steps == nil || *got.Steps != 15 ||
		got.LLMMS == nil || *got.LLMMS != 1_217_000 || got.ToolMS == nil || *got.ToolMS != 503_000 {
		t.Fatalf("timing = %+v", got)
	}
	if got.FirstTokenAvgMS == nil || *got.FirstTokenAvgMS != 4000 ||
		got.FirstTokenCoverage != (domain.FirstTokenCoverage{Covered: 12, Total: 15}) {
		t.Fatalf("first token = %+v coverage %+v", got.FirstTokenAvgMS, got.FirstTokenCoverage)
	}
	// Ratio of sums: 1,680 output tokens / (20,000 ms / 1000) = 84 tok/s.
	if got.OutputTokensPerSecond == nil || *got.OutputTokensPerSecond != 84 {
		t.Fatalf("tok/s = %+v, want 84", got.OutputTokensPerSecond)
	}
	if got.CacheHitRate == nil || *got.CacheHitRate != 2_910_000.0/3_000_000.0 {
		t.Fatalf("cache hit = %+v", got.CacheHitRate)
	}
	if got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 6_300_000 ||
		got.Totals.EstimatedCost == nil || got.Totals.EstimatedCost.TotalNanos != 300 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if store.runtimeCall != 1 {
		t.Fatalf("runtime store calls = %d, want 1", store.runtimeCall)
	}
}

func TestSummaryReaderRuntimeStatsNativeTimingLessKeepsTokens(t *testing.T) {
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "legacy", Mode: domain.SessionModeTUI},
		models: []domain.UsageModelAggregate{{
			Harness: domain.HarnessClaudeCode, ModelID: "claude-sonnet",
			Tokens: testUsageMetrics(1000, 400, 600, 200),
			Cost:   completeCostAggregate(3, 60, 30, 10, 20),
		}},
		// Pre-deployment events carry tokens but no timing rows at all.
		runtimeAgg: domain.SessionRuntimeTimingAggregate{StepCount: 3, TimingRowCount: 0},
	}

	got, err := NewSummaryReader(store).RuntimeStats(context.Background(), "legacy")
	mustNoError(t, err)
	if got.Steps == nil || *got.Steps != 3 {
		t.Fatalf("steps = %+v, want 3 (events exist)", got.Steps)
	}
	// Unknowns must be nil, never zero.
	if got.Rounds != nil || got.LLMMS != nil || got.ToolMS != nil ||
		got.FirstTokenAvgMS != nil || got.OutputTokensPerSecond != nil {
		t.Fatalf("timing-less timing fields = %+v, want all nil", got)
	}
	if got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 1200 ||
		got.Totals.EstimatedCost == nil || got.Totals.EstimatedCost.TotalNanos != 60 {
		t.Fatalf("token/cost totals = %+v, want preserved", got.Totals)
	}
	if got.CacheHitRate == nil || *got.CacheHitRate != 400.0/1000.0 {
		t.Fatalf("cache hit = %+v, want 0.4", got.CacheHitRate)
	}
}

func TestSummaryReaderRuntimeStatsNativeEmptySession(t *testing.T) {
	store := &usageSummaryStoreStub{
		found:      true,
		session:    domain.SessionRecord{ID: "empty", Mode: domain.SessionModeTUI},
		runtimeAgg: domain.SessionRuntimeTimingAggregate{},
	}

	got, err := NewSummaryReader(store).RuntimeStats(context.Background(), "empty")
	mustNoError(t, err)
	if got.Rounds != nil || got.Steps != nil || got.LLMMS != nil || got.ToolMS != nil ||
		got.FirstTokenAvgMS != nil || got.OutputTokensPerSecond != nil ||
		got.CacheHitRate != nil || got.Totals.ProcessedTokens != nil {
		t.Fatalf("empty session stats = %+v, want all unknown", got)
	}
}

func TestSummaryReaderRuntimeStatsUnknownSession(t *testing.T) {
	store := &usageSummaryStoreStub{found: false}
	_, err := NewSummaryReader(store).RuntimeStats(context.Background(), "missing")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestSummaryReaderRuntimeStatsChat(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 0, 5, 0, time.UTC)
	completed := time.Date(2026, 8, 23, 12, 20, 22, 0, time.UTC)
	firstToken := int64(4000)
	secondTurn := completed.Add(time.Minute)
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "chat-1", Mode: domain.SessionModeChat},
		models: []domain.UsageModelAggregate{{
			Harness: domain.HarnessCodex, ModelID: "gpt-5.6",
			Tokens: testUsageMetrics(1000, 400, 600, 200),
		}},
		chatTurns: []domain.ConversationRuntimeTurnFact{
			{
				TurnID: "turn-1", State: domain.TurnStateCompleted,
				RequestedAt: time.Date(2026, 8, 23, 12, 0, 1, 0, time.UTC),
				StartedAt:   &started, CompletedAt: &completed,
				FirstTokenDeltaMS: &firstToken,
				ToolMS:            503_000, ToolKnown: true, AssistantCount: 4, PromptBearing: true,
			},
			{
				TurnID: "turn-2", State: domain.TurnStateCompleted,
				RequestedAt: time.Date(2026, 8, 23, 12, 21, 0, 0, time.UTC),
				StartedAt:   &secondTurn, CompletedAt: &secondTurn,
				FirstTokenDeltaMS: nil, // first content not captured
				ToolMS:            0, ToolKnown: false, AssistantCount: 3, PromptBearing: true,
			},
			{
				// A provider-adopted daemon turn (compaction) is not a round.
				TurnID: "turn-c", State: domain.TurnStateCompleted,
				RequestedAt: time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC),
				StartedAt:   &secondTurn, CompletedAt: &secondTurn,
				ToolMS: 10_000, ToolKnown: true, AssistantCount: 1, PromptBearing: false,
			},
		},
	}

	got, err := NewSummaryReader(store).RuntimeStats(context.Background(), "chat-1")
	mustNoError(t, err)
	if got.Rounds == nil || *got.Rounds != 2 || got.Steps == nil || *got.Steps != 8 {
		t.Fatalf("rounds/steps = %+v/%+v, want 2/8", got.Rounds, got.Steps)
	}
	// LLM remainder per turn: wall (20m17s = 1,217,000ms) minus tool 503,000ms
	// for turn-1; turn-2 is a zero-length terminal turn; the daemon turn's tool
	// time contributes to toolMs but not to rounds.
	wantLLM := int64(1_217_000 - 503_000)
	if got.LLMMS == nil || *got.LLMMS != wantLLM {
		t.Fatalf("llm = %+v, want %d", got.LLMMS, wantLLM)
	}
	if got.ToolMS == nil || *got.ToolMS != 503_000+10_000 {
		t.Fatalf("tool = %+v, want 513000", got.ToolMS)
	}
	if got.FirstTokenAvgMS == nil || *got.FirstTokenAvgMS != 4000 ||
		got.FirstTokenCoverage != (domain.FirstTokenCoverage{Covered: 1, Total: 2}) {
		t.Fatalf("first token = %+v coverage %+v", got.FirstTokenAvgMS, got.FirstTokenCoverage)
	}
	// Chat sessions carry no per-request output token counts, so tok/s is
	// unknown, never zero.
	if got.OutputTokensPerSecond != nil {
		t.Fatalf("chat tok/s = %+v, want nil", got.OutputTokensPerSecond)
	}
	if got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 1200 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

func TestSummaryReaderRuntimeStatsChatNegativeLLMRemainderIsUnknown(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 0, 5, 0, time.UTC)
	completed := time.Date(2026, 8, 23, 12, 0, 20, 0, time.UTC)
	// Tool elapsed (503s) far exceeds the short wall time: the LLM remainder is
	// negative and must be treated as unknown, never reported.
	store := &usageSummaryStoreStub{
		found:   true,
		session: domain.SessionRecord{ID: "chat-2", Mode: domain.SessionModeChat},
		chatTurns: []domain.ConversationRuntimeTurnFact{{
			TurnID: "turn-1", State: domain.TurnStateCompleted,
			RequestedAt: time.Date(2026, 8, 23, 12, 0, 1, 0, time.UTC),
			StartedAt:   &started, CompletedAt: &completed,
			ToolMS: 503_000, ToolKnown: true, AssistantCount: 1, PromptBearing: true,
		}},
	}

	got, err := NewSummaryReader(store).RuntimeStats(context.Background(), "chat-2")
	mustNoError(t, err)
	if got.LLMMS != nil {
		t.Fatalf("llm = %+v, want nil for negative remainder", got.LLMMS)
	}
	if got.ToolMS == nil || *got.ToolMS != 503_000 {
		t.Fatalf("tool = %+v, want 503000", got.ToolMS)
	}
	if got.Rounds == nil || *got.Rounds != 1 {
		t.Fatalf("rounds = %+v, want 1", got.Rounds)
	}
}

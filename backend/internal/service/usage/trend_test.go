package usage

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTrendZeroFillsAbsentBucketsAndHonorsRange(t *testing.T) {
	from := time.Date(2026, 9, 8, 13, 37, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 15, 42, 0, 0, time.UTC)
	store := &usageSummaryStoreStub{trend: []domain.UsageTrendBucket{
		{
			BucketStart: time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC),
			EventCount:  1,
			Tokens:      testUsageMetrics(100, 40, 60, 20),
			Cost:        completeCostAggregate(1, 50, 30, 10, 10),
		},
		{
			BucketStart: time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC),
			EventCount:  2,
			Tokens:      testUsageMetrics(30, 0, 30, 5),
			Cost:        completeCostAggregate(2, 15, 10, 0, 5),
		},
	}}

	got, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", "")
	mustNoError(t, err)
	if store.trendSecs != 3600 || store.trendSrc != "" || store.trendModel != "" {
		t.Fatalf("store call = seconds:%d source:%q model:%q", store.trendSecs, store.trendSrc, store.trendModel)
	}
	if got.BucketSize != domain.UsageTrendBucketHour || len(got.Buckets) != 3 {
		t.Fatalf("trend = %+v", got)
	}
	first, second, third := got.Buckets[0], got.Buckets[1], got.Buckets[2]
	if !first.BucketStart.Equal(time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)) ||
		!third.BucketStart.Equal(time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("bucket starts = %v..%v", first.BucketStart, third.BucketStart)
	}
	if first.RequestCount != 1 || first.InputTokens == nil || *first.InputTokens != 100 ||
		first.CachedInputTokens == nil || *first.CachedInputTokens != 40 ||
		first.UncachedInputTokens == nil || *first.UncachedInputTokens != 60 ||
		first.OutputTokens == nil || *first.OutputTokens != 20 ||
		first.CostNanos == nil || *first.CostNanos != 50 {
		t.Fatalf("first bucket = %+v", first)
	}
	// The 14:00 bucket had no events: explicit zeros, not nil.
	if second.RequestCount != 0 || second.InputTokens == nil || *second.InputTokens != 0 ||
		second.CachedInputTokens == nil || *second.CachedInputTokens != 0 ||
		second.UncachedInputTokens == nil || *second.UncachedInputTokens != 0 ||
		second.OutputTokens == nil || *second.OutputTokens != 0 ||
		second.CostNanos == nil || *second.CostNanos != 0 {
		t.Fatalf("absent bucket = %+v, want explicit zeros", second)
	}
	if third.RequestCount != 2 || third.InputTokens == nil || *third.InputTokens != 30 ||
		third.CostNanos == nil || *third.CostNanos != 15 {
		t.Fatalf("third bucket = %+v", third)
	}
}

func TestTrendKeepsUnknownMetricsNilInPresentBuckets(t *testing.T) {
	from := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	to := from.Add(59 * time.Minute)
	store := &usageSummaryStoreStub{trend: []domain.UsageTrendBucket{{
		BucketStart: from,
		EventCount:  2,
		// Cached input is only known for one of the two events.
		Tokens: domain.UsageTokenMetrics{
			InputTokens:         int64Ptr(10),
			OutputTokens:        int64Ptr(2),
			CachedInputTokens:   nil,
			UncachedInputTokens: int64Ptr(10),
		},
		Cost: domain.UsageCostAggregate{
			EventCount: 2, PricedEventCount: 1, PricedTotalNanos: 3,
			ObservedCostEventCount: 1,
			KnownInputCount:        1, KnownInputNanos: 3,
			UnpricedKnownInputNanos: 1,
		},
	}}}

	got, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", "")
	mustNoError(t, err)
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	bucket := got.Buckets[0]
	if bucket.CachedInputTokens != nil {
		t.Fatalf("partial cached input = %v, want nil", bucket.CachedInputTokens)
	}
	if bucket.InputTokens == nil || *bucket.InputTokens != 10 || bucket.OutputTokens == nil || *bucket.OutputTokens != 2 {
		t.Fatalf("known components = %+v", bucket)
	}
	if bucket.CostNanos == nil || *bucket.CostNanos != 4 {
		t.Fatalf("partial cost = %v, want 3+1 lower bound", bucket.CostNanos)
	}
}

func TestTrendClampsHourToDayForLongRanges(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(60 * 24 * time.Hour)
	store := &usageSummaryStoreStub{trend: []domain.UsageTrendBucket{{
		BucketStart: from,
		EventCount:  1,
		Tokens:      testUsageMetrics(1, 0, 1, 1),
		Cost:        completeCostAggregate(1, 0, 0, 0, 0),
	}}}

	got, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", "")
	mustNoError(t, err)
	if got.BucketSize != domain.UsageTrendBucketDay {
		t.Fatalf("bucket size = %q, want clamped day", got.BucketSize)
	}
	if store.trendSecs != 86400 {
		t.Fatalf("store seconds = %d, want 86400", store.trendSecs)
	}
	// 61 day buckets: Jan 1 through Mar 2 inclusive (60 days of range).
	if len(got.Buckets) != 61 {
		t.Fatalf("bucket count = %d, want 61", len(got.Buckets))
	}
	if !got.Buckets[0].BucketStart.Equal(from) || !got.Buckets[60].BucketStart.Equal(to) {
		t.Fatalf("day bucket starts = %v..%v", got.Buckets[0].BucketStart, got.Buckets[60].BucketStart)
	}
}

func TestTrendRejectsUnboundedRangeAndInvalidBucket(t *testing.T) {
	now := time.Now().UTC()
	reader := NewSummaryReader(&usageSummaryStoreStub{})
	if _, err := reader.Trend(context.Background(), nil, &now, domain.UsageTrendBucketHour, "", ""); err == nil {
		t.Fatal("nil from accepted")
	}
	if _, err := reader.Trend(context.Background(), &now, nil, domain.UsageTrendBucketHour, "", ""); err == nil {
		t.Fatal("nil to accepted")
	}
	after := now.Add(time.Hour)
	if _, err := reader.Trend(context.Background(), &now, &after, domain.UsageTrendBucketSize("week"), "", ""); err == nil {
		t.Fatal("invalid bucket size accepted")
	}
}

func TestTrendPassesThroughFilters(t *testing.T) {
	from := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	store := &usageSummaryStoreStub{}
	_, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketDay, "codex_rollout", "gpt-5.6")
	mustNoError(t, err)
	if store.trendSecs != 86400 || store.trendSrc != "codex_rollout" || store.trendModel != "gpt-5.6" {
		t.Fatalf("store filters = seconds:%d source:%q model:%q", store.trendSecs, store.trendSrc, store.trendModel)
	}
}

func TestTrendPropagatesAggregateOverflow(t *testing.T) {
	from := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	store := &usageSummaryStoreStub{trend: []domain.UsageTrendBucket{{
		BucketStart: from,
		EventCount:  2,
		Tokens:      testUsageMetrics(1, 0, 1, 1),
		Cost: domain.UsageCostAggregate{
			EventCount:              2,
			PricedEventCount:        1,
			PricedTotalNanos:        math.MaxInt64,
			KnownInputCount:         1,
			KnownInputNanos:         1,
			UnpricedKnownInputNanos: 1,
		},
	}}}
	if _, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", ""); err == nil {
		t.Fatal("cost overflow returned nil error")
	}
}

func TestTrendEmptyRangeProducesNoBuckets(t *testing.T) {
	from := time.Date(2026, 9, 8, 13, 37, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	got, err := NewSummaryReader(&usageSummaryStoreStub{}).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", "")
	mustNoError(t, err)
	if len(got.Buckets) != 0 {
		t.Fatalf("buckets = %+v, want none for inverted range", got.Buckets)
	}
}

// The fifth series: present buckets surface the bucket when every event
// carried it, stay nil under partial coverage, and absent buckets zero-fill
// like the other components (ADR 0006 Decision 4).
func TestTrendCarriesCacheCreationSeries(t *testing.T) {
	from := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 15, 30, 0, 0, time.UTC)
	known := testUsageMetrics(30, 7, 23, 4)
	creation := int64(3)
	known.CacheCreationInputTokens = &creation
	store := &usageSummaryStoreStub{trend: []domain.UsageTrendBucket{
		{
			BucketStart: from,
			EventCount:  1,
			Tokens:      known,
			Cost:        completeCostAggregate(1, 50, 30, 7, 4),
		},
		{
			BucketStart: time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC),
			EventCount:  1,
			Tokens:      testUsageMetrics(30, 7, 23, 4),
			Cost:        completeCostAggregate(1, 50, 30, 7, 4),
		},
	}}

	got, err := NewSummaryReader(store).Trend(context.Background(), &from, &to, domain.UsageTrendBucketHour, "", "")
	mustNoError(t, err)
	if len(got.Buckets) != 3 {
		t.Fatalf("buckets = %d, want 3", len(got.Buckets))
	}
	first, absent, partial := got.Buckets[0], got.Buckets[1], got.Buckets[2]
	if first.CacheCreationInputTokens == nil || *first.CacheCreationInputTokens != 3 {
		t.Fatalf("known bucket = %+v, want 3", first)
	}
	if absent.CacheCreationInputTokens == nil || *absent.CacheCreationInputTokens != 0 {
		t.Fatalf("absent bucket = %+v, want explicit zero", absent)
	}
	if partial.CacheCreationInputTokens != nil {
		t.Fatalf("partial bucket = %+v, want nil", *partial.CacheCreationInputTokens)
	}
}

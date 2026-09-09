package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// maxHourTrendRange is the widest range served at hour granularity. Wider
// ranges are clamped to day buckets so a long span never explodes into
// thousands of points.
const maxHourTrendRange = 31 * 24 * time.Hour

// Trend returns a contiguous, UTC-aligned time-bucketed usage series over a
// bounded created_at range, optionally filtered by usage source kind and exact
// model id. Buckets with no events are zero-filled so the chart stays
// continuous; a bucket whose metric is not fully known keeps a nil component
// (nil/unknown never zero). Hour buckets are clamped to day buckets when the
// range spans more than 31 days, and the response echoes the actual size.
// Cache creation is folded into uncached input by the V1 pipeline, so no
// separate cache-creation series is derived.
func (r *SummaryReader) Trend(
	ctx context.Context,
	from, to *time.Time,
	bucketSize domain.UsageTrendBucketSize,
	source, model string,
) (domain.GlobalUsageTrend, error) {
	if r == nil || r.store == nil {
		return domain.GlobalUsageTrend{}, fmt.Errorf("usage summary store is unavailable")
	}
	if from == nil || to == nil {
		return domain.GlobalUsageTrend{}, fmt.Errorf("usage trend requires a bounded range")
	}
	switch bucketSize {
	case domain.UsageTrendBucketHour, domain.UsageTrendBucketDay:
	default:
		return domain.GlobalUsageTrend{}, fmt.Errorf("invalid usage trend bucket size %q", bucketSize)
	}
	if bucketSize == domain.UsageTrendBucketHour && to.Sub(*from) > maxHourTrendRange {
		bucketSize = domain.UsageTrendBucketDay
	}
	seconds := int64(86400)
	if bucketSize == domain.UsageTrendBucketHour {
		seconds = 3600
	}
	rows, err := r.store.AggregateUsageTrend(ctx, from, to, seconds, source, model)
	if err != nil {
		return domain.GlobalUsageTrend{}, err
	}
	byStart := make(map[time.Time]domain.UsageTrendBucket, len(rows))
	for _, row := range rows {
		byStart[row.BucketStart] = row
	}
	buckets, err := zeroFilledUsageTrend(*from, *to, bucketSize, byStart)
	if err != nil {
		return domain.GlobalUsageTrend{}, err
	}
	return domain.GlobalUsageTrend{BucketSize: bucketSize, Buckets: buckets}, nil
}

// zeroFilledUsageTrend builds the contiguous bucket list from the first bucket
// containing from through the bucket containing to. Absent buckets carry
// explicit zeros; present buckets are derived through the same coverage rules
// as the summary.
func zeroFilledUsageTrend(
	from, to time.Time,
	bucketSize domain.UsageTrendBucketSize,
	byStart map[time.Time]domain.UsageTrendBucket,
) ([]domain.UsageTrendBucketTotals, error) {
	width := time.Hour
	if bucketSize == domain.UsageTrendBucketDay {
		width = 24 * time.Hour
	}
	start := floorToBucket(from, width)
	end := floorToBucket(to, width)
	out := make([]domain.UsageTrendBucketTotals, 0)
	for bucketStart := start; !bucketStart.After(end); bucketStart = bucketStart.Add(width) {
		row, ok := byStart[bucketStart]
		if !ok {
			out = append(out, zeroUsageTrendBucket(bucketStart))
			continue
		}
		bucket, err := derivedUsageTrendBucket(row)
		if err != nil {
			return nil, err
		}
		out = append(out, bucket)
	}
	return out, nil
}

func floorToBucket(t time.Time, width time.Duration) time.Time {
	utc := t.UTC()
	if width >= 24*time.Hour {
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	}
	return utc.Truncate(width)
}

func derivedUsageTrendBucket(row domain.UsageTrendBucket) (domain.UsageTrendBucketTotals, error) {
	var costNanos *int64
	estimate, err := estimatedCost(row.Cost)
	if err != nil {
		return domain.UsageTrendBucketTotals{}, err
	}
	if estimate != nil {
		costNanos = &estimate.TotalNanos
	}
	return domain.UsageTrendBucketTotals{
		BucketStart:         row.BucketStart,
		RequestCount:        row.EventCount,
		InputTokens:         row.Tokens.InputTokens,
		CachedInputTokens:   row.Tokens.CachedInputTokens,
		UncachedInputTokens: row.Tokens.UncachedInputTokens,
		OutputTokens:        row.Tokens.OutputTokens,
		CostNanos:           costNanos,
	}, nil
}

func zeroUsageTrendBucket(start time.Time) domain.UsageTrendBucketTotals {
	zero := int64(0)
	return domain.UsageTrendBucketTotals{
		BucketStart:         start,
		InputTokens:         &zero,
		CachedInputTokens:   &zero,
		UncachedInputTokens: &zero,
		OutputTokens:        &zero,
		CostNanos:           &zero,
	}
}

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// UsageSummaryService is the controller-facing compact usage read contract.
type UsageSummaryService interface {
	ListCompact(context.Context, domain.ProjectID) ([]domain.CompactSessionUsage, error)
	Get(context.Context, domain.SessionID) (domain.SessionUsageSummary, error)
	Global(context.Context, *time.Time, *time.Time, string, string) (domain.GlobalUsageSummary, error)
	Models(context.Context, *time.Time, *time.Time, string, string) ([]domain.ModelUsageStatsRow, error)
	Providers(context.Context, *time.Time, *time.Time, string, string) ([]domain.ProviderUsageStatsRow, error)
	ListRequestLog(context.Context, *time.Time, *time.Time, string, string, *int64, int64) (domain.UsageRequestLogPage, error)
	Trend(context.Context, *time.Time, *time.Time, domain.UsageTrendBucketSize, string, string) (domain.GlobalUsageTrend, error)
	RuntimeStats(context.Context, domain.SessionID) (domain.SessionRuntimeStats, error)
}

// UsageController owns compact dashboard usage routes.
type UsageController struct {
	Svc UsageSummaryService
}

// Register mounts usage routes on the supplied router.
func (c *UsageController) Register(r chi.Router) {
	r.Get("/usage/sessions", c.listSessions)
	r.Get("/usage/sessions/{sessionId}", c.getSession)
	r.Get("/usage/sessions/{sessionId}/stats", c.getSessionRuntimeStats)
	r.Get("/usage/summary", c.getSummary)
	r.Get("/usage/models", c.getModelStats)
	r.Get("/usage/providers", c.getProviderStats)
	r.Get("/usage/log", c.getLog)
	r.Get("/usage/trend", c.getTrend)
}

// getSummary returns the global cross-session usage summary over an optional
// created_at range, optionally narrowed by an exact source kind or model id.
// from/to are RFC 3339 timestamps; omitting either leaves that side unbounded.
// Empty source/model strings leave that filter off.
func (c *UsageController) getSummary(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/summary")
		return
	}
	query := r.URL.Query()
	from, err := parseOptionalTime(query.Get("from"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_FROM", "from must be an RFC 3339 timestamp", nil)
		return
	}
	to, err := parseOptionalTime(query.Get("to"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TO", "to must be an RFC 3339 timestamp", nil)
		return
	}
	summary, err := c.Svc.Global(r.Context(), from, to, query.Get("source"), query.Get("model"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, UsageSummaryResponse{
		Totals:       usageTotalsResponse(summary.Totals),
		RequestCount: summary.RequestCount,
		CacheHitRate: summary.CacheHitRate,
		Sources:      usageSourceKindStrings(summary.Sources),
		Models:       summary.Models,
	})
}

func usageSourceKindStrings(kinds []domain.UsageSourceKind) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}

// parseOptionalTime parses an RFC 3339 timestamp, returning nil for an empty
// string and an error for a malformed value.
func parseOptionalTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// getModelStats returns per-model usage rollups over an optional created_at
// range with optional source/model filters. from/to are RFC 3339 timestamps;
// omitting either leaves that side unbounded, and omitting source/model leaves
// that filter unbounded.
func (c *UsageController) getModelStats(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/models")
		return
	}
	query := r.URL.Query()
	from, err := parseOptionalTime(query.Get("from"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_FROM", "from must be an RFC 3339 timestamp", nil)
		return
	}
	to, err := parseOptionalTime(query.Get("to"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TO", "to must be an RFC 3339 timestamp", nil)
		return
	}
	rows, err := c.Svc.Models(r.Context(), from, to, query.Get("source"), query.Get("model"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	models := make([]UsageModelStatsRow, 0, len(rows))
	for _, row := range rows {
		models = append(models, UsageModelStatsRow{
			ModelID: row.ModelID, RequestCount: row.Stats.RequestCount,
			ProcessedTokens: row.Stats.ProcessedTokens, TotalCostNanos: row.Stats.TotalCostNanos,
			AverageCostPerRequestNanos: row.Stats.AverageCostPerRequestNanos,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListUsageModelStatsResponse{Models: models})
}

// getProviderStats returns per-billing-provider usage rollups over an optional
// created_at range with optional source/model filters. Provider rows carry the
// billing attribution source so inferred rows can be displayed honestly.
func (c *UsageController) getProviderStats(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/providers")
		return
	}
	query := r.URL.Query()
	from, err := parseOptionalTime(query.Get("from"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_FROM", "from must be an RFC 3339 timestamp", nil)
		return
	}
	to, err := parseOptionalTime(query.Get("to"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TO", "to must be an RFC 3339 timestamp", nil)
		return
	}
	rows, err := c.Svc.Providers(r.Context(), from, to, query.Get("source"), query.Get("model"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	providers := make([]UsageProviderStatsRow, 0, len(rows))
	for _, row := range rows {
		var attribution *string
		if row.AttributionSource != "" {
			source := string(row.AttributionSource)
			attribution = &source
		}
		providers = append(providers, UsageProviderStatsRow{
			BillingProviderID: row.ProviderID, AttributionSource: attribution,
			RequestCount: row.Stats.RequestCount, ProcessedTokens: row.Stats.ProcessedTokens,
			TotalCostNanos: row.Stats.TotalCostNanos, AverageCostPerRequestNanos: row.Stats.AverageCostPerRequestNanos,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListUsageProviderStatsResponse{Providers: providers})
}

// getLog returns a bounded, newest-first page of usage events for the request
// log. It accepts the same from/to range filters as the summary, optionally
// narrowed by an exact source kind or model id, plus an optional limit and a
// before cursor for keyset paging. Ordering is by event id (descending), which
// is monotonic with insertion, NULL-safe, and stable across pages.
func (c *UsageController) getLog(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/log")
		return
	}
	query := r.URL.Query()
	from, err := parseOptionalTime(query.Get("from"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_FROM", "from must be an RFC 3339 timestamp", nil)
		return
	}
	to, err := parseOptionalTime(query.Get("to"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TO", "to must be an RFC 3339 timestamp", nil)
		return
	}
	source := query.Get("source")
	model := query.Get("model")
	var beforeID *int64
	if raw := query.Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BEFORE", "before must be an event id", nil)
			return
		}
		beforeID = &parsed
	}
	var limit int64 = 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_LIMIT", "limit must be an integer", nil)
			return
		}
		limit = parsed
	}
	page, err := c.Svc.ListRequestLog(r.Context(), from, to, source, model, beforeID, limit)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	items := make([]UsageRequestLogEntryResponse, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, UsageRequestLogEntryResponse{
			ID:                       entry.ID,
			CreatedAt:                entry.CreatedAt,
			BillingProviderID:        nullableString(entry.BillingProviderID),
			ModelID:                  entry.ModelID,
			InputTokens:              entry.InputTokens,
			CachedInputTokens:        entry.CachedInputTokens,
			OutputTokens:             entry.OutputTokens,
			CacheCreationInputTokens: entry.CacheCreationInputTokens,
			EstimatedCostNanos:       entry.EstimatedCostNanos,
			LLMMS:                    entry.LLMMS,
			FirstTokenMS:             entry.FirstTokenMS,
			SourceKind:               string(entry.SourceKind),
			SessionID:                string(entry.SessionID),
			SessionExists:            entry.SessionExists,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, UsageRequestLogResponse{
		Items:        items,
		NextBeforeID: page.NextBeforeID,
	})
}

// getTrend returns a time-bucketed usage series over a required created_at
// range. from/to are RFC 3339 timestamps; bucket selects hour or day buckets
// (default hour); source and model are optional read-time filters matching the
// usage source kind and exact model id.
func (c *UsageController) getTrend(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/trend")
		return
	}
	query := r.URL.Query()
	from, err := parseRequiredTime(query.Get("from"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_FROM", "from must be an RFC 3339 timestamp", nil)
		return
	}
	to, err := parseRequiredTime(query.Get("to"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TO", "to must be an RFC 3339 timestamp", nil)
		return
	}
	bucket := query.Get("bucket")
	if bucket == "" {
		bucket = "hour"
	}
	if bucket != string(domain.UsageTrendBucketHour) && bucket != string(domain.UsageTrendBucketDay) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BUCKET", "bucket must be hour or day", nil)
		return
	}
	trend, err := c.Svc.Trend(r.Context(), from, to, domain.UsageTrendBucketSize(bucket), query.Get("source"), query.Get("model"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, usageTrendResponse(trend))
}

// parseRequiredTime parses an RFC 3339 timestamp, returning an error for an
// empty or malformed value.
func parseRequiredTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, fmt.Errorf("timestamp is required")
	}
	return parseOptionalTime(raw)
}

func usageTrendResponse(trend domain.GlobalUsageTrend) UsageTrendResponse {
	buckets := make([]UsageTrendBucketResponse, 0, len(trend.Buckets))
	for _, bucket := range trend.Buckets {
		buckets = append(buckets, UsageTrendBucketResponse{
			BucketStart:              bucket.BucketStart,
			RequestCount:             bucket.RequestCount,
			InputTokens:              bucket.InputTokens,
			CachedInputTokens:        bucket.CachedInputTokens,
			UncachedInputTokens:      bucket.UncachedInputTokens,
			OutputTokens:             bucket.OutputTokens,
			CacheCreationInputTokens: bucket.CacheCreationInputTokens,
			CostNanos:                bucket.CostNanos,
		})
	}
	return UsageTrendResponse{BucketSize: string(trend.BucketSize), Buckets: buckets}
}

func (c *UsageController) listSessions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/sessions")
		return
	}
	items, err := c.Svc.ListCompact(r.Context(), domain.ProjectID(r.URL.Query().Get("projectId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]CompactSessionUsageResponse, 0, len(items))
	for _, item := range items {
		var totalTokens int64
		if item.ProcessedTokens != nil {
			totalTokens = *item.ProcessedTokens
		}
		out = append(out, CompactSessionUsageResponse{
			SessionID: item.SessionID, ProcessedTokens: item.ProcessedTokens,
			TotalTokens: totalTokens, Incomplete: item.Incomplete,
			EstimatedCost: estimatedCostResponse(item.EstimatedCost),
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListCompactSessionUsageResponse{Sessions: out})
}

func (c *UsageController) getSession(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/sessions/{sessionId}")
		return
	}
	summary, err := c.Svc.Get(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, sessionUsageResponse(summary))
}

// getSessionRuntimeStats returns the per-session runtime statistics bar:
// rounds, steps, LLM/tool time, first-token average, output tok/s, cache-hit
// rate, and token/cost totals, all derived at read time per session mode.
func (c *UsageController) getSessionRuntimeStats(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/usage/sessions/{sessionId}/stats")
		return
	}
	stats, err := c.Svc.RuntimeStats(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, sessionRuntimeStatsResponse(stats))
}

func sessionRuntimeStatsResponse(stats domain.SessionRuntimeStats) SessionRuntimeStatsResponse {
	return SessionRuntimeStatsResponse{
		SessionID:             stats.SessionID,
		Rounds:                stats.Rounds,
		Steps:                 stats.Steps,
		LLMMS:                 stats.LLMMS,
		ToolMS:                stats.ToolMS,
		FirstTokenAvgMS:       stats.FirstTokenAvgMS,
		FirstTokenCoverage:    FirstTokenCoverageResponse{Covered: stats.FirstTokenCoverage.Covered, Total: stats.FirstTokenCoverage.Total},
		OutputTokensPerSecond: stats.OutputTokensPerSecond,
		CacheHitRate:          stats.CacheHitRate,
		Totals:                usageTotalsResponse(stats.Totals),
	}
}

func sessionUsageResponse(summary domain.SessionUsageSummary) SessionUsageResponse {
	harnesses := make([]UsageHarnessResponse, 0, len(summary.Harnesses))
	for _, harness := range summary.Harnesses {
		models := make([]UsageModelResponse, 0, len(harness.Models))
		for _, model := range harness.Models {
			models = append(models, UsageModelResponse{
				ModelID: model.ModelID, Totals: usageTotalsResponse(model.Totals),
			})
		}
		harnesses = append(harnesses, UsageHarnessResponse{
			Harness: string(harness.Harness), Totals: usageTotalsResponse(harness.Totals), Models: models,
		})
	}
	return SessionUsageResponse{
		SessionID: summary.SessionID, Incomplete: summary.Incomplete,
		Totals: usageTotalsResponse(summary.Totals), Harnesses: harnesses,
	}
}

func usageTotalsResponse(totals domain.UsageMetricTotals) UsageTotalsResponse {
	return UsageTotalsResponse{
		InputTokens:              totals.InputTokens,
		CachedInputTokens:        totals.CachedInputTokens,
		UncachedInputTokens:      totals.UncachedInputTokens,
		OutputTokens:             totals.OutputTokens,
		CacheCreationInputTokens: totals.CacheCreationInputTokens,
		ProcessedTokens:          totals.ProcessedTokens,
		CacheReadTokens:          totals.CachedInputTokens,
		EstimatedCost:            estimatedCostResponse(totals.EstimatedCost),
	}
}

func estimatedCostResponse(cost *domain.EstimatedCost) *EstimatedCostResponse {
	if cost == nil {
		return nil
	}
	return &EstimatedCostResponse{
		TotalNanos: cost.TotalNanos, InputNanos: cost.InputNanos,
		CachedInputNanos: cost.CachedInputNanos, OutputNanos: cost.OutputNanos,
		Coverage:            string(cost.Coverage),
		ProviderAttribution: string(cost.ProviderAttribution),
	}
}

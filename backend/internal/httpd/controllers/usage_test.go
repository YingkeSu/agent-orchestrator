package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeUsageSummaryService struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
	items     []domain.CompactSessionUsage
	detail    domain.SessionUsageSummary
	global    domain.GlobalUsageSummary
	from      *time.Time
	to        *time.Time
	source    string
	model     string
	err       error

	runtimeStats domain.SessionRuntimeStats

	models         []domain.ModelUsageStatsRow
	providers      []domain.ProviderUsageStatsRow
	modelFrom      *time.Time
	modelTo        *time.Time
	modelSource    string
	modelModel     string
	providerFrom   *time.Time
	providerTo     *time.Time
	providerSource string
	providerModel  string
	logPage        domain.UsageRequestLogPage
	logFrom        *time.Time
	logTo          *time.Time
	logSource      string
	logModel       string
	logBefore      *int64
	logLimit       int64

	trend      domain.GlobalUsageTrend
	trendFrom  *time.Time
	trendTo    *time.Time
	trendSize  domain.UsageTrendBucketSize
	trendSrc   string
	trendModel string
}

func (f *fakeUsageSummaryService) ListCompact(_ context.Context, projectID domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	f.projectID = projectID
	return f.items, f.err
}

func (f *fakeUsageSummaryService) Get(_ context.Context, sessionID domain.SessionID) (domain.SessionUsageSummary, error) {
	f.sessionID = sessionID
	return f.detail, f.err
}

func (f *fakeUsageSummaryService) RuntimeStats(_ context.Context, sessionID domain.SessionID) (domain.SessionRuntimeStats, error) {
	f.sessionID = sessionID
	return f.runtimeStats, f.err
}

func (f *fakeUsageSummaryService) Global(_ context.Context, from, to *time.Time, source, model string) (domain.GlobalUsageSummary, error) {
	f.from, f.to, f.source, f.model = from, to, source, model
	return f.global, f.err
}

func (f *fakeUsageSummaryService) ListRequestLog(_ context.Context, from, to *time.Time, source, model string, beforeID *int64, limit int64) (domain.UsageRequestLogPage, error) {
	f.logFrom, f.logTo, f.logSource, f.logModel, f.logBefore, f.logLimit = from, to, source, model, beforeID, limit
	return f.logPage, f.err
}

func (f *fakeUsageSummaryService) Trend(_ context.Context, from, to *time.Time, size domain.UsageTrendBucketSize, source, model string) (domain.GlobalUsageTrend, error) {
	f.trendFrom, f.trendTo, f.trendSize, f.trendSrc, f.trendModel = from, to, size, source, model
	return f.trend, f.err
}

func newUsageTestServer(t *testing.T, svc *fakeUsageSummaryService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{UsageSummary: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUsageAPIListsCompactProjectUsage(t *testing.T) {
	inputCost := int64(300000000)
	processed := int64(12300)
	unavailableProcessed := int64(3)
	svc := &fakeUsageSummaryService{items: []domain.CompactSessionUsage{
		{
			SessionID: "reverb-12", ProcessedTokens: &processed, Incomplete: true,
			EstimatedCost: &domain.EstimatedCost{
				TotalNanos: 420000000, InputNanos: &inputCost,
				Coverage:            domain.EstimatedCostCoveragePartial,
				ProviderAttribution: domain.EstimatedCostProviderAttributionInferred,
			},
		},
		{SessionID: "unavailable", ProcessedTokens: &unavailableProcessed},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/sessions?projectId=reverb", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.projectID != "reverb" {
		t.Fatalf("project id = %q, want reverb", svc.projectID)
	}
	var got struct {
		Sessions []struct {
			SessionID       string          `json:"sessionId"`
			ProcessedTokens int64           `json:"processedTokens"`
			TotalTokens     int64           `json:"totalTokens"`
			Incomplete      bool            `json:"incomplete"`
			EstimatedCost   json.RawMessage `json:"estimatedCost"`
		} `json:"sessions"`
	}
	mustJSON(t, body, &got)
	if len(got.Sessions) != 2 || got.Sessions[0].SessionID != "reverb-12" ||
		got.Sessions[0].ProcessedTokens != 12300 || got.Sessions[0].TotalTokens != 12300 ||
		!got.Sessions[0].Incomplete {
		t.Fatalf("response = %+v", got)
	}
	var cost struct {
		TotalNanos          int64  `json:"totalNanos"`
		InputNanos          *int64 `json:"inputNanos"`
		CachedInputNanos    *int64 `json:"cachedInputNanos"`
		Coverage            string `json:"coverage"`
		ProviderAttribution string `json:"providerAttribution"`
	}
	mustJSON(t, got.Sessions[0].EstimatedCost, &cost)
	if cost.TotalNanos != 420000000 || cost.InputNanos == nil || *cost.InputNanos != 300000000 ||
		cost.CachedInputNanos != nil || cost.Coverage != "partial" || cost.ProviderAttribution != "inferred" {
		t.Fatalf("estimated cost = %+v", cost)
	}
	if string(got.Sessions[1].EstimatedCost) != "null" {
		t.Fatalf("unavailable estimatedCost = %s, want explicit null", got.Sessions[1].EstimatedCost)
	}
}

func TestUsageAPIShowsDetailedEstimatedCostAndProviderAttribution(t *testing.T) {
	input := int64(1000)
	uncached := int64(600)
	output := int64(200)
	zero := int64(0)
	cachedInput := int64(400)
	processed := int64(1200)
	svc := &fakeUsageSummaryService{detail: domain.SessionUsageSummary{
		SessionID: "reverb-12", Incomplete: true,
		Totals: domain.UsageMetricTotals{
			InputTokens: &input, CachedInputTokens: &cachedInput, UncachedInputTokens: &uncached,
			OutputTokens: &output, ProcessedTokens: &processed,
			EstimatedCost: &domain.EstimatedCost{
				TotalNanos: 135, InputNanos: &input, CachedInputNanos: &zero,
				OutputNanos: &output, Coverage: domain.EstimatedCostCoveragePartial,
				ProviderAttribution: domain.EstimatedCostProviderAttributionMixed,
			},
		},
		Harnesses: []domain.HarnessUsageSummary{{
			Harness: domain.HarnessCodex,
			Models: []domain.ModelUsageSummary{{
				ModelID: "gpt-5.6",
				Totals: domain.UsageMetricTotals{EstimatedCost: &domain.EstimatedCost{
					TotalNanos: 0, InputNanos: &zero, CachedInputNanos: &zero,
					OutputNanos: &zero, Coverage: domain.EstimatedCostCoverageComplete,
					ProviderAttribution: domain.EstimatedCostProviderAttributionObserved,
				}},
			}},
		}},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/sessions/reverb-12", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.sessionID != "reverb-12" {
		t.Fatalf("session id = %q", svc.sessionID)
	}
	// Provider-shaped counters and per-metric provenance are no longer projected
	// onto this boundary; the bounded provider object owns them now.
	for _, forbidden := range []string{
		`"cost"`, `"valueNanos"`, `"pricingVersion"`,
		`"provenance"`, `"providerDetails"`, `"cacheWriteTokens"`, `"reasoningTokens"`,
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("detailed usage exposed %s: %s", forbidden, body)
		}
	}
	var got struct {
		SessionID  string `json:"sessionId"`
		Incomplete bool   `json:"incomplete"`
		Totals     struct {
			InputTokens         int64 `json:"inputTokens"`
			CachedInputTokens   int64 `json:"cachedInputTokens"`
			UncachedInputTokens int64 `json:"uncachedInputTokens"`
			OutputTokens        int64 `json:"outputTokens"`
			ProcessedTokens     int64 `json:"processedTokens"`
			CacheReadTokens     int64 `json:"cacheReadTokens"`
			EstimatedCost       struct {
				TotalNanos          int64  `json:"totalNanos"`
				InputNanos          *int64 `json:"inputNanos"`
				CachedInputNanos    *int64 `json:"cachedInputNanos"`
				Coverage            string `json:"coverage"`
				ProviderAttribution string `json:"providerAttribution"`
			} `json:"estimatedCost"`
		} `json:"totals"`
		Harnesses []struct {
			Models []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
				Totals     struct {
					EstimatedCost struct {
						TotalNanos          int64  `json:"totalNanos"`
						Coverage            string `json:"coverage"`
						ProviderAttribution string `json:"providerAttribution"`
					} `json:"estimatedCost"`
				} `json:"totals"`
			} `json:"models"`
		} `json:"harnesses"`
	}
	mustJSON(t, body, &got)
	if got.SessionID != "reverb-12" || !got.Incomplete || got.Totals.InputTokens != 1000 ||
		got.Totals.EstimatedCost.TotalNanos != 135 ||
		got.Totals.EstimatedCost.InputNanos == nil || *got.Totals.EstimatedCost.InputNanos != 1000 ||
		got.Totals.EstimatedCost.CachedInputNanos == nil || *got.Totals.EstimatedCost.CachedInputNanos != 0 ||
		got.Totals.EstimatedCost.Coverage != "partial" ||
		got.Totals.EstimatedCost.ProviderAttribution != "mixed" ||
		got.Totals.CachedInputTokens != 400 || got.Totals.UncachedInputTokens != 600 ||
		got.Totals.OutputTokens != 200 ||
		got.Totals.ProcessedTokens != 1200 || got.Totals.CacheReadTokens != 400 ||
		len(got.Harnesses) != 1 || len(got.Harnesses[0].Models) != 1 ||
		got.Harnesses[0].Models[0].ModelID != "gpt-5.6" ||
		got.Harnesses[0].Models[0].Totals.EstimatedCost.TotalNanos != 0 ||
		got.Harnesses[0].Models[0].Totals.EstimatedCost.Coverage != "complete" ||
		got.Harnesses[0].Models[0].Totals.EstimatedCost.ProviderAttribution != "observed" {
		t.Fatalf("response = %+v", got)
	}
}

func TestUsageAPIReturnsGlobalSummary(t *testing.T) {
	input := int64(1100)
	cachedInput := int64(400)
	uncachedInput := int64(700)
	output := int64(200)
	processed := int64(1300)
	rate := 400.0 / 1100.0
	svc := &fakeUsageSummaryService{global: domain.GlobalUsageSummary{
		RequestCount: 2,
		CacheHitRate: &rate,
		Totals: domain.UsageMetricTotals{
			InputTokens: &input, CachedInputTokens: &cachedInput, UncachedInputTokens: &uncachedInput,
			OutputTokens: &output, ProcessedTokens: &processed,
			EstimatedCost: &domain.EstimatedCost{
				TotalNanos: 300, Coverage: domain.EstimatedCostCoverageComplete,
				ProviderAttribution: domain.EstimatedCostProviderAttributionObserved,
			},
		},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/summary?from=2026-09-01T00:00:00Z&to=2026-09-08T00:00:00Z", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.from == nil || svc.from.Format(time.RFC3339) != "2026-09-01T00:00:00Z" ||
		svc.to == nil || svc.to.Format(time.RFC3339) != "2026-09-08T00:00:00Z" {
		t.Fatalf("range = %v .. %v", svc.from, svc.to)
	}
	var got struct {
		RequestCount int64    `json:"requestCount"`
		CacheHitRate *float64 `json:"cacheHitRate"`
		Totals       struct {
			InputTokens         *int64 `json:"inputTokens"`
			CachedInputTokens   *int64 `json:"cachedInputTokens"`
			UncachedInputTokens *int64 `json:"uncachedInputTokens"`
			OutputTokens        *int64 `json:"outputTokens"`
			ProcessedTokens     *int64 `json:"processedTokens"`
			EstimatedCost       struct {
				TotalNanos          int64  `json:"totalNanos"`
				Coverage            string `json:"coverage"`
				ProviderAttribution string `json:"providerAttribution"`
			} `json:"estimatedCost"`
		} `json:"totals"`
	}
	mustJSON(t, body, &got)
	if got.RequestCount != 2 || got.CacheHitRate == nil || *got.CacheHitRate != 400.0/1100.0 {
		t.Fatalf("summary = %+v", got)
	}
	if got.Totals.InputTokens == nil || *got.Totals.InputTokens != 1100 ||
		got.Totals.CachedInputTokens == nil || *got.Totals.CachedInputTokens != 400 ||
		got.Totals.UncachedInputTokens == nil || *got.Totals.UncachedInputTokens != 700 ||
		got.Totals.OutputTokens == nil || *got.Totals.OutputTokens != 200 ||
		got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 1300 ||
		got.Totals.EstimatedCost.TotalNanos != 300 ||
		got.Totals.EstimatedCost.Coverage != "complete" ||
		got.Totals.EstimatedCost.ProviderAttribution != "observed" {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

func TestUsageSummaryAPIAppliesSourceAndModelFilters(t *testing.T) {
	svc := &fakeUsageSummaryService{global: domain.GlobalUsageSummary{
		RequestCount: 1,
		Sources:      []domain.UsageSourceKind{domain.UsageSourceCodexRollout},
		Models:       []string{"gpt-5.6"},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/summary?source=codex_rollout&model=gpt-5.6", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.source != "codex_rollout" || svc.model != "gpt-5.6" {
		t.Fatalf("filters = source:%q model:%q", svc.source, svc.model)
	}
	var got struct {
		Sources []string `json:"sources"`
		Models  []string `json:"models"`
	}
	mustJSON(t, body, &got)
	if len(got.Sources) != 1 || got.Sources[0] != "codex_rollout" ||
		len(got.Models) != 1 || got.Models[0] != "gpt-5.6" {
		t.Fatalf("dimensions = %+v", got)
	}
}

func TestUsageSummaryAPIUnknownFilterYieldsEmptyNotError(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/summary?source=no_such_source&model=no-such-model", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		RequestCount int64    `json:"requestCount"`
		Sources      []string `json:"sources"`
		Models       []string `json:"models"`
	}
	mustJSON(t, body, &got)
	if got.RequestCount != 0 || len(got.Sources) != 0 || len(got.Models) != 0 {
		t.Fatalf("unknown filter summary = %+v, want empty zero response", got)
	}
}

func TestUsageSummaryAPIReturnsNullCacheHitRateWhenUnknown(t *testing.T) {
	svc := &fakeUsageSummaryService{global: domain.GlobalUsageSummary{RequestCount: 1}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/summary", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		CacheHitRate json.RawMessage `json:"cacheHitRate"`
	}
	mustJSON(t, body, &got)
	if string(got.CacheHitRate) != "null" {
		t.Fatalf("cacheHitRate = %s, want explicit null", got.CacheHitRate)
	}
}

func TestUsageSummaryAPIRejectsMalformedRange(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	for _, path := range []string{
		"/api/v1/usage/summary?from=not-a-time",
		"/api/v1/usage/summary?to=2026-13-99T00:00:00Z",
	} {
		_, status, _ := doRequest(t, srv, http.MethodGet, path, "")
		if status != http.StatusBadRequest {
			t.Fatalf("status for %q = %d, want 400", path, status)
		}
	}
}

func TestUsageAPIReturnsRequestLogPage(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	input, cachedInput, output, cost := int64(1100), int64(400), int64(200), int64(135)
	llmMs, firstTokenMs := int64(43_000), int64(3_400)
	before := int64(10)
	svc := &fakeUsageSummaryService{logPage: domain.UsageRequestLogPage{
		Items: []domain.UsageRequestLogEntry{
			{
				ID: 5, CreatedAt: &now, BillingProviderID: "anthropic", ModelID: "claude-sonnet",
				InputTokens: &input, CachedInputTokens: &cachedInput, OutputTokens: &output,
				EstimatedCostNanos: &cost, LLMMS: &llmMs, FirstTokenMS: &firstTokenMs,
				SourceKind: domain.UsageSourceClaudeMain,
				SessionID:  "reverb-12", SessionExists: true,
			},
		},
		NextBeforeID: &before,
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/log?from=2026-09-01T00:00:00Z&to=2026-09-08T00:00:00Z&source=codex_rollout&model=gpt-5&limit=20&before=99", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.logFrom == nil || svc.logFrom.Format(time.RFC3339) != "2026-09-01T00:00:00Z" ||
		svc.logTo == nil || svc.logTo.Format(time.RFC3339) != "2026-09-08T00:00:00Z" ||
		svc.logSource != "codex_rollout" || svc.logModel != "gpt-5" ||
		svc.logBefore == nil || *svc.logBefore != 99 || svc.logLimit != 20 {
		t.Fatalf("log params = from %v to %v source %q model %q before %v limit %d", svc.logFrom, svc.logTo, svc.logSource, svc.logModel, svc.logBefore, svc.logLimit)
	}
	var got struct {
		Items []struct {
			ID                 int64   `json:"id"`
			CreatedAt          string  `json:"createdAt"`
			BillingProviderID  *string `json:"billingProviderId"`
			ModelID            string  `json:"modelId"`
			InputTokens        *int64  `json:"inputTokens"`
			CachedInputTokens  *int64  `json:"cachedInputTokens"`
			OutputTokens       *int64  `json:"outputTokens"`
			EstimatedCostNanos *int64  `json:"estimatedCostNanos"`
			LLMMS              *int64  `json:"llmMs"`
			FirstTokenMS       *int64  `json:"firstTokenMs"`
			SourceKind         string  `json:"sourceKind"`
			SessionID          string  `json:"sessionId"`
			SessionExists      bool    `json:"sessionExists"`
		} `json:"items"`
		NextBeforeID *int64 `json:"nextBeforeId"`
	}
	mustJSON(t, body, &got)
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v", got.Items)
	}
	item := got.Items[0]
	if item.ID != 5 || item.BillingProviderID == nil || *item.BillingProviderID != "anthropic" ||
		item.ModelID != "claude-sonnet" || item.InputTokens == nil || *item.InputTokens != 1100 ||
		item.CachedInputTokens == nil || *item.CachedInputTokens != 400 ||
		item.OutputTokens == nil || *item.OutputTokens != 200 ||
		item.EstimatedCostNanos == nil || *item.EstimatedCostNanos != 135 ||
		item.LLMMS == nil || *item.LLMMS != 43_000 ||
		item.FirstTokenMS == nil || *item.FirstTokenMS != 3_400 ||
		item.SourceKind != "claude_main" || item.SessionID != "reverb-12" || !item.SessionExists {
		t.Fatalf("item = %+v", item)
	}
	if got.NextBeforeID == nil || *got.NextBeforeID != 10 {
		t.Fatalf("nextBeforeId = %v, want 10", got.NextBeforeID)
	}
}

func TestUsageAPIReturnsNullBillingProviderWhenUnattributed(t *testing.T) {
	svc := &fakeUsageSummaryService{logPage: domain.UsageRequestLogPage{
		Items: []domain.UsageRequestLogEntry{
			{
				ID: 1, ModelID: "gpt-5.6",
				SourceKind: domain.UsageSourceCodexRollout, SessionID: "mer-1",
			},
		},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/log", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		Items []struct {
			CreatedAt         json.RawMessage `json:"createdAt"`
			BillingProviderID json.RawMessage `json:"billingProviderId"`
			InputTokens       json.RawMessage `json:"inputTokens"`
			LLMMS             json.RawMessage `json:"llmMs"`
			FirstTokenMS      json.RawMessage `json:"firstTokenMs"`
			SessionExists     bool            `json:"sessionExists"`
		} `json:"items"`
	}
	mustJSON(t, body, &got)
	if len(got.Items) != 1 || string(got.Items[0].CreatedAt) != "null" ||
		string(got.Items[0].BillingProviderID) != "null" ||
		string(got.Items[0].InputTokens) != "null" ||
		string(got.Items[0].LLMMS) != "null" ||
		string(got.Items[0].FirstTokenMS) != "null" || got.Items[0].SessionExists {
		t.Fatalf("unattributed item = %+v", got.Items)
	}
}

func TestUsageLogAPIRejectsMalformedParams(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	for _, path := range []string{
		"/api/v1/usage/log?from=not-a-time",
		"/api/v1/usage/log?to=2026-13-99T00:00:00Z",
		"/api/v1/usage/log?before=not-a-number",
		"/api/v1/usage/log?limit=abc",
	} {
		_, status, _ := doRequest(t, srv, http.MethodGet, path, "")
		if status != http.StatusBadRequest {
			t.Fatalf("status for %q = %d, want 400", path, status)
		}
	}
}

func TestUsageLogAPIDefaultsLimitWhenOmitted(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	_, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/log", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if svc.logLimit != 50 {
		t.Fatalf("default limit = %d, want 50", svc.logLimit)
	}
	if svc.logFrom != nil || svc.logTo != nil || svc.logBefore != nil {
		t.Fatalf("default log params = from %v to %v before %v, want all nil", svc.logFrom, svc.logTo, svc.logBefore)
	}
}

func TestUsageAPIReturnsUsageTrend(t *testing.T) {
	first := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	zero, cost := int64(0), int64(50000000)
	uncached, cached, output := int64(600), int64(400), int64(200)
	svc := &fakeUsageSummaryService{trend: domain.GlobalUsageTrend{
		BucketSize: domain.UsageTrendBucketHour,
		Buckets: []domain.UsageTrendBucketTotals{
			{
				BucketStart: first, RequestCount: 2,
				InputTokens: ptrInt64(1000), CachedInputTokens: &cached,
				UncachedInputTokens: &uncached, OutputTokens: &output, CostNanos: &cost,
			},
			{
				BucketStart: second, RequestCount: 0,
				InputTokens: &zero, CachedInputTokens: &zero,
				UncachedInputTokens: &zero, OutputTokens: &zero, CostNanos: &zero,
			},
		},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet,
		"/api/v1/usage/trend?from=2026-09-08T13:37:00Z&to=2026-09-08T15:42:00Z&bucket=hour", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.trendFrom == nil || svc.trendFrom.Format(time.RFC3339) != "2026-09-08T13:37:00Z" ||
		svc.trendTo == nil || svc.trendTo.Format(time.RFC3339) != "2026-09-08T15:42:00Z" ||
		svc.trendSize != domain.UsageTrendBucketHour {
		t.Fatalf("trend params = %v .. %v size %q", svc.trendFrom, svc.trendTo, svc.trendSize)
	}
	var got struct {
		BucketSize string `json:"bucketSize"`
		Buckets    []struct {
			BucketStart         string `json:"bucketStart"`
			RequestCount        int64  `json:"requestCount"`
			InputTokens         *int64 `json:"inputTokens"`
			CachedInputTokens   *int64 `json:"cachedInputTokens"`
			UncachedInputTokens *int64 `json:"uncachedInputTokens"`
			OutputTokens        *int64 `json:"outputTokens"`
			CostNanos           *int64 `json:"costNanos"`
		} `json:"buckets"`
	}
	mustJSON(t, body, &got)
	if got.BucketSize != "hour" || len(got.Buckets) != 2 {
		t.Fatalf("trend = %+v", got)
	}
	if got.Buckets[0].BucketStart != "2026-09-08T13:00:00Z" || got.Buckets[0].RequestCount != 2 ||
		got.Buckets[0].CachedInputTokens == nil || *got.Buckets[0].CachedInputTokens != 400 ||
		got.Buckets[0].CostNanos == nil || *got.Buckets[0].CostNanos != 50000000 {
		t.Fatalf("first bucket = %+v", got.Buckets[0])
	}
	if got.Buckets[1].RequestCount != 0 || got.Buckets[1].CostNanos == nil || *got.Buckets[1].CostNanos != 0 {
		t.Fatalf("zero-filled bucket = %+v, want explicit zero cost", got.Buckets[1])
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestUsageTrendAPIRequiresAndValidatesParams(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	for _, path := range []string{
		"/api/v1/usage/trend?to=2026-09-08T15:42:00Z",
		"/api/v1/usage/trend?from=2026-09-08T13:37:00Z",
		"/api/v1/usage/trend?from=not-a-time&to=2026-09-08T15:42:00Z",
		"/api/v1/usage/trend?from=2026-09-08T13:37:00Z&to=2026-13-99T00:00:00Z",
		"/api/v1/usage/trend?from=2026-09-08T13:37:00Z&to=2026-09-08T15:42:00Z&bucket=week",
	} {
		_, status, _ := doRequest(t, srv, http.MethodGet, path, "")
		if status != http.StatusBadRequest {
			t.Fatalf("status for %q = %d, want 400", path, status)
		}
	}
}

func TestUsageTrendAPIDefaultsBucketAndPassesFilters(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)
	body, status, _ := doRequest(t, srv, http.MethodGet,
		"/api/v1/usage/trend?from=2026-09-08T13:37:00Z&to=2026-09-08T15:42:00Z&source=codex_rollout&model=gpt-5.6", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.trendSize != domain.UsageTrendBucketHour {
		t.Fatalf("default bucket size = %q, want hour", svc.trendSize)
	}
	if svc.trendSrc != "codex_rollout" || svc.trendModel != "gpt-5.6" {
		t.Fatalf("trend filters = source:%q model:%q", svc.trendSrc, svc.trendModel)
	}
}

func TestUsageAPIReturnsSessionRuntimeStats(t *testing.T) {
	rounds, steps, llm, tool, firstToken := int64(11), int64(117), int64(1_217_000), int64(503_000), int64(4000)
	rate := 84.0
	cacheRate := 0.97
	input, cachedInput, uncachedInput, output, processed := int64(3_000_000), int64(2_910_000), int64(90_000), int64(3_300_000), int64(6_300_000)
	svc := &fakeUsageSummaryService{runtimeStats: domain.SessionRuntimeStats{
		SessionID:             "reverb-12",
		Rounds:                &rounds,
		Steps:                 &steps,
		LLMMS:                 &llm,
		ToolMS:                &tool,
		FirstTokenAvgMS:       &firstToken,
		FirstTokenCoverage:    domain.FirstTokenCoverage{Covered: 12, Total: 15},
		OutputTokensPerSecond: &rate,
		CacheHitRate:          &cacheRate,
		Totals: domain.UsageMetricTotals{
			InputTokens: &input, CachedInputTokens: &cachedInput, UncachedInputTokens: &uncachedInput,
			OutputTokens: &output, ProcessedTokens: &processed,
		},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/sessions/reverb-12/stats", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.sessionID != "reverb-12" {
		t.Fatalf("session id = %q", svc.sessionID)
	}
	var got struct {
		SessionID          string `json:"sessionId"`
		Rounds             *int64 `json:"rounds"`
		Steps              *int64 `json:"steps"`
		LLMMS              *int64 `json:"llmMs"`
		ToolMS             *int64 `json:"toolMs"`
		FirstTokenAvgMS    *int64 `json:"firstTokenAvgMs"`
		FirstTokenCoverage struct {
			Covered int64 `json:"covered"`
			Total   int64 `json:"total"`
		} `json:"firstTokenCoverage"`
		OutputTokensPerSecond *float64 `json:"outputTokensPerSecond"`
		CacheHitRate          *float64 `json:"cacheHitRate"`
		Totals                struct {
			ProcessedTokens *int64 `json:"processedTokens"`
		} `json:"totals"`
	}
	mustJSON(t, body, &got)
	if got.SessionID != "reverb-12" || got.Rounds == nil || *got.Rounds != 11 ||
		got.Steps == nil || *got.Steps != 117 || got.LLMMS == nil || *got.LLMMS != 1_217_000 ||
		got.ToolMS == nil || *got.ToolMS != 503_000 || got.FirstTokenAvgMS == nil || *got.FirstTokenAvgMS != 4000 ||
		got.FirstTokenCoverage.Covered != 12 || got.FirstTokenCoverage.Total != 15 ||
		got.OutputTokensPerSecond == nil || *got.OutputTokensPerSecond != 84 ||
		got.CacheHitRate == nil || *got.CacheHitRate != 0.97 ||
		got.Totals.ProcessedTokens == nil || *got.Totals.ProcessedTokens != 6_300_000 {
		t.Fatalf("response = %+v", got)
	}
}

func TestUsageAPIReturnsSessionRuntimeStatsWithUnknowns(t *testing.T) {
	svc := &fakeUsageSummaryService{runtimeStats: domain.SessionRuntimeStats{SessionID: "reverb-12"}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/sessions/reverb-12/stats", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		Rounds             *int64          `json:"rounds"`
		Steps              *int64          `json:"steps"`
		LLMMS              *int64          `json:"llmMs"`
		FirstTokenAvgMS    *int64          `json:"firstTokenAvgMs"`
		OutputTokensPerSec *float64        `json:"outputTokensPerSecond"`
		CacheHitRate       json.RawMessage `json:"cacheHitRate"`
	}
	mustJSON(t, body, &got)
	// Timing figures without facts must serialize as explicit nulls, never
	// zeroes; an absent coverage pair stays zero-valued.
	if got.Rounds != nil || got.Steps != nil || got.LLMMS != nil ||
		got.FirstTokenAvgMS != nil || got.OutputTokensPerSec != nil {
		t.Fatalf("unknown timing fields = %+v, want all null", got)
	}
	if string(got.CacheHitRate) != "null" {
		t.Fatalf("cacheHitRate = %s, want explicit null", got.CacheHitRate)
	}
}

func TestUsageAPISessionRuntimeStatsNotFound(t *testing.T) {
	svc := &fakeUsageSummaryService{err: apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")}
	srv := newUsageTestServer(t, svc)

	_, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/sessions/nope/stats", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

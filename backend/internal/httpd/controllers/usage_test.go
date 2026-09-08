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
)

type fakeUsageSummaryService struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
	items     []domain.CompactSessionUsage
	detail    domain.SessionUsageSummary
	global    domain.GlobalUsageSummary
	from      *time.Time
	to        *time.Time
	err       error
}

func (f *fakeUsageSummaryService) ListCompact(_ context.Context, projectID domain.ProjectID) ([]domain.CompactSessionUsage, error) {
	f.projectID = projectID
	return f.items, f.err
}

func (f *fakeUsageSummaryService) Get(_ context.Context, sessionID domain.SessionID) (domain.SessionUsageSummary, error) {
	f.sessionID = sessionID
	return f.detail, f.err
}

func (f *fakeUsageSummaryService) Global(_ context.Context, from, to *time.Time) (domain.GlobalUsageSummary, error) {
	f.from, f.to = from, to
	return f.global, f.err
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

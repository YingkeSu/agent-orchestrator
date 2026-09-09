package controllers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func (f *fakeUsageSummaryService) Models(_ context.Context, from, to *time.Time, source, model string) ([]domain.ModelUsageStatsRow, error) {
	f.modelFrom, f.modelTo, f.modelSource, f.modelModel = from, to, source, model
	return f.models, f.err
}

func (f *fakeUsageSummaryService) Providers(_ context.Context, from, to *time.Time, source, model string) ([]domain.ProviderUsageStatsRow, error) {
	f.providerFrom, f.providerTo, f.providerSource, f.providerModel = from, to, source, model
	return f.providers, f.err
}

func TestUsageAPIReturnsModelStatsWithFilters(t *testing.T) {
	processed := int64(200)
	totalCost := int64(210)
	average := 105.0
	svc := &fakeUsageSummaryService{models: []domain.ModelUsageStatsRow{
		{ModelID: "gpt-5", Stats: domain.UsageAggregateStats{
			RequestCount: 2, ProcessedTokens: &processed,
			TotalCostNanos: &totalCost, AverageCostPerRequestNanos: &average,
		}},
		{ModelID: "unknown-cost", Stats: domain.UsageAggregateStats{RequestCount: 1}},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/models?from=2026-09-01T00:00:00Z&to=2026-09-08T00:00:00Z&source=claude_main&model=claude-x", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.modelFrom == nil || svc.modelFrom.Format(time.RFC3339) != "2026-09-01T00:00:00Z" ||
		svc.modelTo == nil || svc.modelTo.Format(time.RFC3339) != "2026-09-08T00:00:00Z" ||
		svc.modelSource != "claude_main" || svc.modelModel != "claude-x" {
		t.Fatalf("filters = %v..%v source %q model %q", svc.modelFrom, svc.modelTo, svc.modelSource, svc.modelModel)
	}
	var got struct {
		Models []struct {
			ModelID                    string          `json:"modelId"`
			RequestCount               int64           `json:"requestCount"`
			ProcessedTokens            json.RawMessage `json:"processedTokens"`
			TotalCostNanos             json.RawMessage `json:"totalCostNanos"`
			AverageCostPerRequestNanos *float64        `json:"avgCostPerRequestNanos"`
		} `json:"models"`
	}
	mustJSON(t, body, &got)
	if len(got.Models) != 2 || got.Models[0].ModelID != "gpt-5" || got.Models[0].RequestCount != 2 ||
		string(got.Models[0].ProcessedTokens) != "200" || string(got.Models[0].TotalCostNanos) != "210" ||
		got.Models[0].AverageCostPerRequestNanos == nil || *got.Models[0].AverageCostPerRequestNanos != 105 {
		t.Fatalf("model rows = %+v", got.Models)
	}
	if got.Models[1].ModelID != "unknown-cost" || string(got.Models[1].ProcessedTokens) != "null" ||
		string(got.Models[1].TotalCostNanos) != "null" || got.Models[1].AverageCostPerRequestNanos != nil {
		t.Fatalf("unknown cost row = %+v", got.Models[1])
	}
}

func TestUsageAPIReturnsProviderStatsWithAttribution(t *testing.T) {
	processed := int64(200)
	totalCost := int64(210)
	average := 105.0
	svc := &fakeUsageSummaryService{providers: []domain.ProviderUsageStatsRow{
		{
			ProviderID: "openai", AttributionSource: domain.EstimatedCostProviderAttributionMixed,
			Stats: domain.UsageAggregateStats{
				RequestCount: 2, ProcessedTokens: &processed,
				TotalCostNanos: &totalCost, AverageCostPerRequestNanos: &average,
			},
		},
		{
			ProviderID: "", AttributionSource: "",
			Stats: domain.UsageAggregateStats{RequestCount: 1},
		},
	}}
	srv := newUsageTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/usage/providers", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var got struct {
		Providers []struct {
			BillingProviderID string          `json:"billingProviderId"`
			AttributionSource json.RawMessage `json:"attributionSource"`
			RequestCount      int64           `json:"requestCount"`
		} `json:"providers"`
	}
	mustJSON(t, body, &got)
	if len(got.Providers) != 2 || got.Providers[0].BillingProviderID != "openai" ||
		string(got.Providers[0].AttributionSource) != `"mixed"` || got.Providers[0].RequestCount != 2 {
		t.Fatalf("provider rows = %+v", got.Providers)
	}
	if got.Providers[1].BillingProviderID != "" || string(got.Providers[1].AttributionSource) != "null" {
		t.Fatalf("unattributed row = %+v", got.Providers[1])
	}
}

func TestUsageAggregateAPIRejectsMalformedRange(t *testing.T) {
	svc := &fakeUsageSummaryService{}
	srv := newUsageTestServer(t, svc)

	for _, path := range []string{
		"/api/v1/usage/models?from=not-a-time",
		"/api/v1/usage/models?to=2026-13-99T00:00:00Z",
		"/api/v1/usage/providers?from=not-a-time",
		"/api/v1/usage/providers?to=2026-13-99T00:00:00Z",
	} {
		_, status, _ := doRequest(t, srv, http.MethodGet, path, "")
		if status != http.StatusBadRequest {
			t.Fatalf("status for %q = %d, want 400", path, status)
		}
	}
}

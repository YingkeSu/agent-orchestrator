package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestAggregateUsageByModelAndProviderRollsUpAcrossSessionsAndFilters seeds
// priced usage events across two sources, models, and billing providers, then
// asserts the per-model and per-provider rollups over the unbounded range, the
// optional source/model filters, and the created_at range (including an empty
// range).
func TestAggregateUsageByModelAndProviderRollsUpAcrossSessionsAndFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	day1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	codexSession := seedUsageSession(t, s, domain.HarnessCodex)
	codexSource := seedUsageSource(t, s, codexSession, day1)
	claudeSession := seedUsageSession(t, s, domain.HarnessClaudeCode)
	claudeSource := seedUsageSource(t, s, claudeSession, day2)

	// Day 1 codex events: gpt-5 twice (one observed, one inferred price) and
	// gpt-5.1 once, served by a different billing provider.
	gptObserved := usageEvent("gpt-observed", canonicalUsageTokens(100, 40, 60, 30))
	gptObserved.BillingProviderID = "openai"
	gptObserved.BillingProviderSource = domain.UsageBillingProviderObserved
	gptObserved.CreatedAt = day1
	gptObserved.Costs = pricedCosts(100, 20, 30, 150)

	gptInferred := usageEvent("gpt-inferred", canonicalUsageTokens(50, 10, 40, 20))
	gptInferred.BillingProviderID = "openai"
	gptInferred.BillingProviderSource = domain.UsageBillingProviderInferred
	gptInferred.CreatedAt = day1
	gptInferred.Costs = pricedCosts(30, 5, 25, 60)

	gpt51 := usageEvent("gpt51", canonicalUsageTokens(10, 5, 5, 5))
	gpt51.ModelID = "gpt-5.1"
	gpt51.BillingProviderID = "zai"
	gpt51.BillingProviderSource = domain.UsageBillingProviderObserved
	gpt51.CreatedAt = day1
	gpt51.Costs = pricedCosts(20, 10, 10, 40)

	mustNoError(t, s.ApplyUsageChunk(ctx, codexSource.ID, 0, codexSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`, UpdatedAt: day1,
	}, []domain.ModelUsageEvent{gptObserved, gptInferred, gpt51}), "seed codex events")

	// Day 2 claude events: one attributed and priced, one unattributed.
	claudePriced := anthropicUsageEvent("claude-priced", 20, 10, 40, 15)
	claudePriced.BillingProviderID = "anthropic"
	claudePriced.BillingProviderSource = domain.UsageBillingProviderObserved
	claudePriced.CreatedAt = day2
	claudePriced.Costs = pricedCosts(20, 5, 5, 30)

	claudeUnattributed := anthropicUsageEvent("claude-unattributed", 5, 0, 0, 2)
	claudeUnattributed.CreatedAt = day2

	mustNoError(t, s.ApplyUsageChunk(ctx, claudeSource.ID, 0, claudeSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`, UpdatedAt: day2,
	}, []domain.ModelUsageEvent{claudePriced, claudeUnattributed}), "seed claude events")

	t.Run("per-model unbounded", func(t *testing.T) {
		models, err := s.AggregateUsageByModel(ctx, nil, nil, "", "")
		mustNoError(t, err)
		if len(models) != 3 {
			t.Fatalf("model rows = %d, want 3: %+v", len(models), models)
		}
		// The store returns rows in GROUP BY key order; the service applies the
		// cost-descending display order.
		assertModelRow(t, models, 0, "claude-x", 2, 92, 30)
		assertModelRow(t, models, 1, "gpt-5", 2, 200, 210)
		assertModelRow(t, models, 2, "gpt-5.1", 1, 15, 40)
	})

	t.Run("per-provider unbounded", func(t *testing.T) {
		providers, err := s.AggregateUsageByProvider(ctx, nil, nil, "", "")
		mustNoError(t, err)
		if len(providers) != 4 {
			t.Fatalf("provider rows = %d, want 4: %+v", len(providers), providers)
		}
		// The unattributed claude event lands in the empty bucket, so request
		// counts never silently vanish from the provider view.
		assertProviderRow(t, providers, 0, "", 1, 7, 0, 0, 0)
		assertProviderRow(t, providers, 1, "anthropic", 1, 85, 30, 1, 0)
		assertProviderRow(t, providers, 2, "openai", 2, 200, 210, 1, 1)
		assertProviderRow(t, providers, 3, "zai", 1, 15, 40, 1, 0)
	})

	t.Run("source filter", func(t *testing.T) {
		models, err := s.AggregateUsageByModel(ctx, nil, nil, string(domain.UsageSourceClaudeMain), "")
		mustNoError(t, err)
		if len(models) != 1 || models[0].ModelID != "claude-x" || models[0].Cost.EventCount != 2 {
			t.Fatalf("claude-main models = %+v", models)
		}
		providers, err := s.AggregateUsageByProvider(ctx, nil, nil, string(domain.UsageSourceCodexRollout), "")
		mustNoError(t, err)
		if len(providers) != 2 {
			t.Fatalf("codex providers = %+v", providers)
		}
		unknown, err := s.AggregateUsageByModel(ctx, nil, nil, "nonexistent", "")
		mustNoError(t, err)
		if len(unknown) != 0 {
			t.Fatalf("unknown source rows = %+v, want empty", unknown)
		}
	})

	t.Run("model filter", func(t *testing.T) {
		models, err := s.AggregateUsageByModel(ctx, nil, nil, "", "gpt-5")
		mustNoError(t, err)
		if len(models) != 1 || models[0].ModelID != "gpt-5" || models[0].Cost.EventCount != 2 {
			t.Fatalf("gpt-5 models = %+v", models)
		}
		providers, err := s.AggregateUsageByProvider(ctx, nil, nil, "", "gpt-5")
		mustNoError(t, err)
		if len(providers) != 1 || providers[0].ProviderID != "openai" {
			t.Fatalf("gpt-5 providers = %+v", providers)
		}
	})

	t.Run("range", func(t *testing.T) {
		day1End := day1.Add(24 * time.Hour)
		models, err := s.AggregateUsageByModel(ctx, &day1, &day1End, "", "")
		mustNoError(t, err)
		if len(models) != 2 {
			t.Fatalf("day1 models = %+v", models)
		}
		after := day2.Add(48 * time.Hour)
		emptyModels, err := s.AggregateUsageByModel(ctx, &after, &after, "", "")
		mustNoError(t, err)
		if len(emptyModels) != 0 {
			t.Fatalf("empty range models = %+v, want empty", emptyModels)
		}
		emptyProviders, err := s.AggregateUsageByProvider(ctx, &after, &after, "", "")
		mustNoError(t, err)
		if len(emptyProviders) != 0 {
			t.Fatalf("empty range providers = %+v, want empty", emptyProviders)
		}
	})
}

func pricedCosts(input, cachedInput, output, total int64) domain.UsageEventCosts {
	return domain.UsageEventCosts{
		InputCostNanos: &input, CachedInputCostNanos: &cachedInput,
		OutputCostNanos: &output, EstimatedCostNanos: &total, PricingVersion: "catalog-v1",
	}
}

func assertModelRow(t *testing.T, rows []domain.UsageModelScopeAggregate, index int, model string, events, processedTokens, totalCost int64) {
	t.Helper()
	row := rows[index]
	if row.ModelID != model || row.Cost.EventCount != events {
		t.Fatalf("model row %d = %+v, want %q with %d events", index, row, model, events)
	}
	if usageTokenValue(row.Tokens.InputTokens)+usageTokenValue(row.Tokens.OutputTokens) != processedTokens {
		t.Fatalf("model row %d processed = %d+%d, want %d", index, usageTokenValue(row.Tokens.InputTokens), usageTokenValue(row.Tokens.OutputTokens), processedTokens)
	}
	if row.Cost.PricedTotalNanos != totalCost {
		t.Fatalf("model row %d cost = %d, want %d", index, row.Cost.PricedTotalNanos, totalCost)
	}
}

func assertProviderRow(t *testing.T, rows []domain.UsageProviderScopeAggregate, index int, provider string, events, processedTokens, totalCost, observed, inferred int64) {
	t.Helper()
	row := rows[index]
	if row.ProviderID != provider || row.Cost.EventCount != events {
		t.Fatalf("provider row %d = %+v, want %q with %d events", index, row, provider, events)
	}
	if usageTokenValue(row.Tokens.InputTokens)+usageTokenValue(row.Tokens.OutputTokens) != processedTokens {
		t.Fatalf("provider row %d processed = %d+%d, want %d", index, usageTokenValue(row.Tokens.InputTokens), usageTokenValue(row.Tokens.OutputTokens), processedTokens)
	}
	if row.Cost.PricedTotalNanos != totalCost {
		t.Fatalf("provider row %d cost = %d, want %d", index, row.Cost.PricedTotalNanos, totalCost)
	}
	if row.ObservedEventCount != observed || row.InferredEventCount != inferred {
		t.Fatalf("provider row %d attribution = observed %d / inferred %d, want %d/%d", index, row.ObservedEventCount, row.InferredEventCount, observed, inferred)
	}
}

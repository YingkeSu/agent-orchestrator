package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// runtimeClock is the base instant every runtime-stats fixture derives from.
var runtimeClock = time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)

// TestAggregateSessionRuntimeTimingRollsUpRootRoundsOnly covers the native
// session rollup: steps count every event, rounds count only distinct root
// generation rounds (subagent sources never add rounds), durations and the
// ratio-of-sums rate inputs sum only the known values.
func TestAggregateSessionRuntimeTimingRollsUpRootRoundsOnly(t *testing.T) {
	s := newTestStore(t)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, runtimeClock)

	llm1, tool1, first1 := int64(4_000), int64(800), int64(1_500)
	llm2, first2 := int64(6_000), int64(2_500)
	subLLM, subFirst := int64(9_000), int64(500)
	rootOne := anthropicUsageEvent("root-1", 10, 0, 0, 100)
	rootTwo := anthropicUsageEvent("root-2", 10, 0, 0, 68)
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: runtimeClock,
	}, []domain.ModelUsageEvent{rootOne, rootTwo}, []domain.UsageEventTiming{
		{SourceEventKey: rootOne.SourceEventKey, RoundSeq: 1, LLMMS: &llm1, ToolMS: &tool1, FirstTokenMS: &first1},
		{SourceEventKey: rootTwo.SourceEventKey, RoundSeq: 2, LLMMS: &llm2, FirstTokenMS: &first2},
	}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	// A subagent source reuses the same round ordinals as its root (each
	// source counts rounds independently), so its events must add steps and
	// time but never a third round.
	subSource := mustInsertUsageSource(t, s, runtimeClock, domain.UsageSourceRecord{
		BindingID:       source.BindingID,
		Kind:            domain.UsageSourceClaudeSubagent,
		NativeSessionID: "child-thread",
		SubagentID:      "reviewer",
		ArtifactPath:    "/tmp/claude/subagent.jsonl",
		FileIdentity:    "dev:ino",
		State:           domain.UsageSourcePending,
	})
	if err := s.ApplyUsageChunk(context.Background(), subSource.ID, 0, subSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: runtimeClock,
	}, []domain.ModelUsageEvent{anthropicUsageEvent("subagent-1", 10, 0, 0, 5)}, []domain.UsageEventTiming{
		{SourceEventKey: "subagent-1", RoundSeq: 1, LLMMS: &subLLM, FirstTokenMS: &subFirst},
	}); err != nil {
		t.Fatalf("apply subagent chunk: %v", err)
	}

	agg, err := s.AggregateSessionRuntimeTiming(context.Background(), session.ID)
	mustNoError(t, err)
	if agg.StepCount != 3 || agg.TimingRowCount != 3 {
		t.Fatalf("steps/timing rows = %d/%d, want 3/3", agg.StepCount, agg.TimingRowCount)
	}
	// Only the root source's two round ordinals count; the subagent's round 1
	// must not add a third.
	if agg.RoundCount != 2 {
		t.Fatalf("round count = %d, want 2", agg.RoundCount)
	}
	// LLM sums every known duration (root + subagent); tool sums only root-1.
	if agg.LLMMSTotal == nil || *agg.LLMMSTotal != 4_000+6_000+9_000 {
		t.Fatalf("llm total = %+v", agg.LLMMSTotal)
	}
	if agg.ToolMSTotal == nil || *agg.ToolMSTotal != 800 {
		t.Fatalf("tool total = %+v", agg.ToolMSTotal)
	}
	if agg.FirstTokenKnown != 3 || agg.FirstTokenMSSum == nil || *agg.FirstTokenMSSum != 1_500+2_500+500 {
		t.Fatalf("first token = known %d sum %+v", agg.FirstTokenKnown, agg.FirstTokenMSSum)
	}
	// Ratio-of-sums inputs: every event here has a positive LLM elapsed and
	// known output tokens.
	if agg.RateOutputTokens == nil || *agg.RateOutputTokens != 100+68+5 ||
		agg.RateLLMMS == nil || *agg.RateLLMMS != 19_000 {
		t.Fatalf("rate inputs = %+v/%+v", agg.RateOutputTokens, agg.RateLLMMS)
	}
}

// TestAggregateSessionRuntimeTimingRateExcludesUncoveredEvents covers the rate
// denominator rule: an event with a NULL LLM elapsed or unknown output tokens
// contributes to neither rate sum (ADR Decision 3).
func TestAggregateSessionRuntimeTimingRateExcludesUncoveredEvents(t *testing.T) {
	s := newTestStore(t)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, runtimeClock)

	llm := int64(2_000)
	covered := anthropicUsageEvent("covered", 10, 0, 0, 40)
	noLLM := anthropicUsageEvent("no-llm", 10, 0, 0, 60)
	zeroLLM := anthropicUsageEvent("zero-llm", 10, 0, 0, 80)
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: runtimeClock,
	}, []domain.ModelUsageEvent{covered, noLLM, zeroLLM}, []domain.UsageEventTiming{
		{SourceEventKey: covered.SourceEventKey, RoundSeq: 1, LLMMS: &llm},
		{SourceEventKey: noLLM.SourceEventKey, RoundSeq: 1},
		{SourceEventKey: zeroLLM.SourceEventKey, RoundSeq: 1, LLMMS: int64Ptr(0)},
	}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	agg, err := s.AggregateSessionRuntimeTiming(context.Background(), session.ID)
	mustNoError(t, err)
	if agg.StepCount != 3 {
		t.Fatalf("steps = %d, want 3", agg.StepCount)
	}
	// Only the covered event feeds the rate: a NULL LLM elapsed and a known
	// zero LLM elapsed are both excluded ("never infinitely fast").
	if agg.RateOutputTokens == nil || *agg.RateOutputTokens != 40 ||
		agg.RateLLMMS == nil || *agg.RateLLMMS != 2_000 {
		t.Fatalf("rate inputs = %+v/%+v, want 40/2000", agg.RateOutputTokens, agg.RateLLMMS)
	}
	// A known zero LLM elapsed still sums into the session total.
	if agg.LLMMSTotal == nil || *agg.LLMMSTotal != 2_000 {
		t.Fatalf("llm total = %+v, want 2000", agg.LLMMSTotal)
	}
}

// TestAggregateSessionRuntimeTimingTimingLessSession covers the
// timing-less-session degradation: tokens exist (events exist) but no timing
// rows were captured, so every timing rollup reports unknown, never zero.
func TestAggregateSessionRuntimeTimingTimingLessSession(t *testing.T) {
	s := newTestStore(t)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, runtimeClock)

	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: runtimeClock,
	}, []domain.ModelUsageEvent{anthropicUsageEvent("legacy-1", 10, 0, 0, 5)}, nil); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	agg, err := s.AggregateSessionRuntimeTiming(context.Background(), session.ID)
	mustNoError(t, err)
	if agg.StepCount != 1 || agg.TimingRowCount != 0 || agg.RoundCount != 0 {
		t.Fatalf("counts = %+v", agg)
	}
	if agg.LLMMSTotal != nil || agg.ToolMSTotal != nil || agg.FirstTokenMSSum != nil ||
		agg.RateOutputTokens != nil || agg.RateLLMMS != nil {
		t.Fatalf("timing totals = %+v, want all nil", agg)
	}
}

// TestAggregateSessionRuntimeTimingEmptySession covers the empty-session case:
// no usage events at all leaves every figure at its zero/unknown value.
func TestAggregateSessionRuntimeTimingEmptySession(t *testing.T) {
	s := newTestStore(t)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)

	agg, err := s.AggregateSessionRuntimeTiming(context.Background(), session.ID)
	mustNoError(t, err)
	if agg.StepCount != 0 || agg.RoundCount != 0 || agg.LLMMSTotal != nil ||
		agg.ToolMSTotal != nil || agg.FirstTokenMSSum != nil {
		t.Fatalf("empty session aggregate = %+v", agg)
	}
}

// TestListConversationRuntimeTurnFacts covers the chat-mode per-turn facts:
// prompt-bearing flags, assistant step counts, first-token deltas measured from
// requested_at, terminal tool elapsed, and the discarded-turn exclusions.
func TestListConversationRuntimeTurnFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "stats")
	rec := sampleRecord("stats")
	rec.Mode = domain.SessionModeChat
	session, err := s.CreateSession(ctx, rec)
	mustNoError(t, err, "create chat session")
	conversation, err := s.CreateConversation(ctx, "stats-conv", domain.ConversationScopeSession, "stats", session.ID, runtimeClock)
	mustNoError(t, err, "create conversation")
	mustNoError(t, s.ClaimChatControllerGeneration(ctx, session.ID, "gen-1", runtimeClock))

	// Turn 1: a human prompt, six seconds to first content, an eight-second
	// command activity, twenty minutes of wall time.
	turn1At := runtimeClock.Add(time.Minute)
	created, err := s.AppendUserMessage(ctx, conversation.ID, session.ID, "gen-1", domain.ConversationMessage{
		ID: "user-1", Text: "tighten the sidebar", Origin: domain.MessageOriginHuman,
	}, "turn-1", turn1At)
	if err != nil || !created {
		t.Fatalf("append user-1: created=%v err=%v", created, err)
	}
	mustNoError(t, s.BindTurnToProvider(ctx, "turn-1", "prov-1", turn1At.Add(2*time.Second)))
	mustNoError(t, s.AppendAssistantDelta(ctx, conversation.ID, "item-1", "prov-1", "sure", "assistant-1", turn1At.Add(6*time.Second)))
	if err := s.UpsertActivity(ctx, conversation.ID, "prov-1", domain.ConversationActivity{
		ID: "act-1", Kind: domain.ActivityKindCommand, Status: domain.ActivityStatusRunning,
		Summary: "npm test", ProviderItemID: "act-1",
	}, turn1At.Add(10*time.Second)); err != nil {
		t.Fatalf("start activity: %v", err)
	}
	if err := s.UpsertActivity(ctx, conversation.ID, "prov-1", domain.ConversationActivity{
		ID: "act-1", Kind: domain.ActivityKindCommand, Status: domain.ActivityStatusCompleted,
		Summary: "npm test", ProviderItemID: "act-1",
	}, turn1At.Add(18*time.Second)); err != nil {
		t.Fatalf("settle activity: %v", err)
	}
	mustNoError(t, s.SettleTurn(ctx, conversation.ID, "prov-1", domain.TurnStateCompleted, "", turn1At.Add(20*time.Minute)))

	// Turn 2: an automation prompt with one assistant message and no captured
	// first content (its assistant message belongs to no provider turn).
	turn2At := turn1At.Add(30 * time.Minute)
	created, err = s.AppendUserMessage(ctx, conversation.ID, session.ID, "gen-1", domain.ConversationMessage{
		ID: "user-2", Text: "nightly sweep", Origin: domain.MessageOriginAutomation,
	}, "turn-2", turn2At)
	if err != nil || !created {
		t.Fatalf("append user-2: created=%v err=%v", created, err)
	}
	mustNoError(t, s.BindTurnToProvider(ctx, "turn-2", "prov-2", turn2At))
	mustNoError(t, s.AppendAssistantDelta(ctx, conversation.ID, "item-2", "prov-2", "done", "assistant-2", turn2At.Add(time.Second)))
	mustNoError(t, s.AppendAssistantDelta(ctx, conversation.ID, "item-3", "prov-2", "also", "assistant-3", turn2At.Add(2*time.Second)))
	mustNoError(t, s.SettleTurn(ctx, conversation.ID, "prov-2", domain.TurnStateCompleted, "", turn2At.Add(5*time.Minute)))

	// Turn 3: a daemon-only (provider-adopted) compaction turn — never a round.
	mustNoError(t, s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "gen-1", "turn-3", "prov-3", turn2At.Add(10*time.Minute)))

	// Turn 4: a queued prompt the user withdrew before dispatch.
	turn4At := turn2At.Add(20 * time.Minute)
	created, err = s.AppendUserMessage(ctx, conversation.ID, session.ID, "gen-1", domain.ConversationMessage{
		ID: "user-4", Text: "withdrawn", Origin: domain.MessageOriginHuman,
	}, "turn-4", turn4At)
	if err != nil || !created {
		t.Fatalf("append user-4: created=%v err=%v", created, err)
	}
	mustNoError(t, s.CancelQueuedTurnByID(ctx, conversation.ID, "turn-4", turn4At.Add(time.Second)))

	facts, err := s.ListConversationRuntimeTurnFacts(ctx, session.ID)
	mustNoError(t, err)
	if len(facts) != 3 {
		t.Fatalf("turn facts = %d, want 3 (cancelled turn discarded)", len(facts))
	}
	byID := make(map[string]domain.ConversationRuntimeTurnFact, len(facts))
	for _, fact := range facts {
		byID[fact.TurnID] = fact
	}

	turn1 := byID["turn-1"]
	if !turn1.PromptBearing || turn1.AssistantCount != 1 || turn1.State != domain.TurnStateCompleted {
		t.Fatalf("turn 1 = %+v", turn1)
	}
	if turn1.FirstTokenDeltaMS == nil || *turn1.FirstTokenDeltaMS != 6_000 {
		t.Fatalf("turn 1 first token delta = %d ms, want 6000 (requested=%s)", usageTokenValue(turn1.FirstTokenDeltaMS), turn1.RequestedAt)
	}
	if !turn1.ToolKnown || turn1.ToolMS != 8_000 {
		t.Fatalf("turn 1 tool = known %v ms %d, want known 8000", turn1.ToolKnown, turn1.ToolMS)
	}
	if turn1.StartedAt == nil || turn1.CompletedAt == nil {
		t.Fatalf("turn 1 bounds missing: %+v", turn1)
	}

	turn2 := byID["turn-2"]
	if !turn2.PromptBearing || turn2.AssistantCount != 2 {
		t.Fatalf("turn 2 = %+v", turn2)
	}
	// The first content arrived one second after requested_at.
	if turn2.FirstTokenDeltaMS == nil || *turn2.FirstTokenDeltaMS != 1_000 {
		t.Fatalf("turn 2 first token delta = %+v, want 1000", turn2.FirstTokenDeltaMS)
	}
	if turn2.ToolKnown {
		t.Fatalf("turn 2 tool known = true, want false (no activities)")
	}

	turn3 := byID["turn-3"]
	if turn3.PromptBearing {
		t.Fatalf("daemon-only turn counted as a round: %+v", turn3)
	}
}

// TestListConversationRuntimeTurnFactsNoConversation covers the no-conversation
// case: a session without a conversation has no chat facts at all.
func TestListConversationRuntimeTurnFactsNoConversation(t *testing.T) {
	s := newTestStore(t)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)

	facts, err := s.ListConversationRuntimeTurnFacts(context.Background(), session.ID)
	mustNoError(t, err)
	if len(facts) != 0 {
		t.Fatalf("facts = %d, want 0", len(facts))
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

package usage

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestClaudeTimingDerivation covers the timing ADR Decision 1 Claude Code rules:
// LLM elapsed is the assistant usage record minus the preceding user record,
// first-token is that same interval when the user record started a round, and
// tool elapsed is the following tool_result user record minus the assistant
// record. Anchors that predate capture stay nil.
func TestClaudeTimingDerivation(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"fix the test"}}`)},
		{Offset: 100, Data: []byte(`{"type":"assistant","uuid":"one","timestamp":"2026-07-01T10:00:04Z","message":{"id":"msg-1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
		{Offset: 200, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}}`)},
		{Offset: 300, Data: []byte(`{"type":"assistant","uuid":"two","timestamp":"2026-07-01T10:00:16Z","message":{"id":"msg-2","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`)},
	}

	result := parseRecords(source, records, 400, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 || len(result.Timing) != 2 {
		t.Fatalf("events=%d timing=%d, want 2/2", len(result.Events), len(result.Timing))
	}
	first := result.Timing[0]
	if first.SourceEventKey != result.Events[0].SourceEventKey ||
		first.RoundSeq != 1 ||
		usageTimingValue(first.LLMMS) != 4000 ||
		usageTimingValue(first.FirstTokenMS) != 4000 ||
		usageTimingValue(first.ToolMS) != 8000 {
		t.Fatalf("first timing = %+v", first)
	}
	second := result.Timing[1]
	if second.RoundSeq != 1 ||
		usageTimingValue(second.LLMMS) != 4000 ||
		second.FirstTokenMS != nil ||
		second.ToolMS != nil {
		t.Fatalf("second timing = %+v, want llm only (no prompt anchor, no tool result)", second)
	}
}

// TestClaudeTimingMissingAnchorsStayNil covers the certification rule: an
// assistant usage record with no preceding user record (a subagent transcript
// that starts mid-conversation) has nil LLM/first-token, and a tool_use
// completion with no following tool_result has nil tool elapsed.
func TestClaudeTimingMissingAnchorsStayNil(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeSubagent)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"assistant","uuid":"one","timestamp":"2026-07-01T10:00:00Z","message":{"id":"msg-1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
	}

	result := parseRecords(source, records, 100, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 || len(result.Timing) != 1 {
		t.Fatalf("events=%d timing=%d, want 1/1", len(result.Events), len(result.Timing))
	}
	got := result.Timing[0]
	if got.LLMMS != nil || got.FirstTokenMS != nil || got.ToolMS != nil {
		t.Fatalf("timing = %+v, want all nil for missing anchors", got)
	}
	if got.RoundSeq != 1 {
		t.Fatalf("round seq = %d, want 1 (rounds always start at 1)", got.RoundSeq)
	}
	// The pending tool anchor must be durable in parser state so a later chunk
	// can close tool elapsed.
	state := parserStateFromResult(t, result, domain.UsageSourceClaudeSubagent)
	if state.Claude.Timing.PendingTool == nil ||
		state.Claude.Timing.PendingTool.SourceEventKey != result.Events[0].SourceEventKey {
		t.Fatalf("pending tool anchor = %+v, want the emitted event", state.Claude.Timing.PendingTool)
	}
}

// TestClaudeTimingCrossChunkToolClose covers the pending tool anchor: an
// assistant tool_use completion read in one chunk is closed by the tool_result
// user record of a later chunk, emitting a timing refresh for the committed
// event key.
func TestClaudeTimingCrossChunkToolClose(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	firstChunk := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"do it"}}`)},
		{Offset: 100, Data: []byte(`{"type":"assistant","uuid":"one","timestamp":"2026-07-01T10:00:04Z","message":{"id":"msg-1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
	}
	first := parseRecords(source, firstChunk, 200, time.Unix(1700000000, 0).UTC())
	if len(first.Events) != 1 || len(first.Timing) != 1 {
		t.Fatalf("first chunk events=%d timing=%d", len(first.Events), len(first.Timing))
	}
	if first.Timing[0].ToolMS != nil {
		t.Fatalf("first chunk tool = %v, want nil (tool_result not read yet)", usageTimingValue(first.Timing[0].ToolMS))
	}

	state, err := decodeParserState(domain.UsageSourceRecord{
		Kind:            domain.UsageSourceClaudeMain,
		ParserStateJSON: first.Cursor.ParserStateJSON,
	})
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	second := parseRecordsWithState(source, []jsonlRecord{
		{Offset: 200, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}}`)},
	}, 300, time.Unix(1700000000, 0).UTC(), state)
	if len(second.Events) != 0 || len(second.Timing) != 1 {
		t.Fatalf("second chunk events=%d timing=%d, want 0 events and one close", len(second.Events), len(second.Timing))
	}
	closed := second.Timing[0]
	if closed.SourceEventKey != first.Events[0].SourceEventKey ||
		usageTimingValue(closed.ToolMS) != 8000 ||
		usageTimingValue(closed.LLMMS) != 4000 ||
		usageTimingValue(closed.FirstTokenMS) != 4000 {
		t.Fatalf("closed timing = %+v", closed)
	}
}

// TestClaudeTimingRoundsIncrementOnHumanPrompts covers the round boundary rule:
// a new human prompt between steps starts a new round.
func TestClaudeTimingRoundsIncrementOnHumanPrompts(t *testing.T) {
	source := usageSource(domain.UsageSourceClaudeMain)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"first"}}`)},
		{Offset: 100, Data: []byte(`{"type":"assistant","uuid":"one","timestamp":"2026-07-01T10:00:04Z","message":{"id":"msg-1","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}}`)},
		{Offset: 200, Data: []byte(`{"type":"user","timestamp":"2026-07-01T10:00:30Z","message":{"role":"user","content":"second"}}`)},
		{Offset: 300, Data: []byte(`{"type":"assistant","uuid":"two","timestamp":"2026-07-01T10:00:34Z","message":{"id":"msg-2","model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`)},
	}
	result := parseRecords(source, records, 400, time.Unix(1700000000, 0).UTC())
	if len(result.Timing) != 2 {
		t.Fatalf("timing = %d, want 2", len(result.Timing))
	}
	if result.Timing[0].RoundSeq != 1 || result.Timing[1].RoundSeq != 2 {
		t.Fatalf("round seqs = %d/%d, want 1/2", result.Timing[0].RoundSeq, result.Timing[1].RoundSeq)
	}
}

// TestCodexTimingDerivation covers the Codex rollout rules: LLM elapsed is the
// token_count usage record minus the turn_context anchor, and first-token is
// the turn anchor to the first assistant message response, attached to the
// first usage event of the turn.
func TestCodexTimingDerivation(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"turn_context","timestamp":"2026-07-01T10:00:00Z","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 100, Data: []byte(`{"type":"response_item","timestamp":"2026-07-01T10:00:03Z","payload":{"type":"message","role":"assistant","content":[]}}`)},
		{Offset: 200, Data: codexTokenLine("2026-07-01T10:00:05Z", 100, 60, 0, 20, 5)},
		{Offset: 300, Data: codexTokenLine("2026-07-01T10:00:09Z", 160, 90, 0, 35, 8)},
	}
	result := parseRecords(source, records, 400, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 2 || len(result.Timing) != 2 {
		t.Fatalf("events=%d timing=%d, want 2/2", len(result.Events), len(result.Timing))
	}
	first := result.Timing[0]
	if first.RoundSeq != 1 || usageTimingValue(first.LLMMS) != 5000 || usageTimingValue(first.FirstTokenMS) != 3000 {
		t.Fatalf("first timing = %+v", first)
	}
	second := result.Timing[1]
	if second.RoundSeq != 1 || usageTimingValue(second.LLMMS) != 9000 || second.FirstTokenMS != nil {
		t.Fatalf("second timing = %+v, want llm only", second)
	}
}

// TestCodexTimingMissingTurnAnchorStaysNil covers the certification rule for a
// Codex source whose turn_context predates capture: usage events have nil LLM
// and first-token.
func TestCodexTimingMissingTurnAnchorStaysNil(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	records := []jsonlRecord{
		{Offset: 0, Data: codexTokenLine("2026-07-01T10:00:05Z", 100, 60, 0, 20, 5)},
	}
	result := parseRecords(source, records, 100, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 || len(result.Timing) != 1 {
		t.Fatalf("events=%d timing=%d", len(result.Events), len(result.Timing))
	}
	if result.Timing[0].LLMMS != nil || result.Timing[0].FirstTokenMS != nil || result.Timing[0].RoundSeq != 1 {
		t.Fatalf("timing = %+v, want nil anchors with round 1", result.Timing[0])
	}
}

// TestCodexTimingFirstResponseAfterFirstEventCovers the ordering where the
// token_count usage event is read before the turn's first assistant message
// response: the response closes the first-token via a timing refresh for the
// already-emitted event.
func TestCodexTimingFirstResponseAfterFirstEvent(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	first := parseRecords(source, []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"turn_context","timestamp":"2026-07-01T10:00:00Z","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 100, Data: codexTokenLine("2026-07-01T10:00:05Z", 100, 60, 0, 20, 5)},
	}, 200, time.Unix(1700000000, 0).UTC())
	if len(first.Events) != 1 || len(first.Timing) != 1 || first.Timing[0].FirstTokenMS != nil {
		t.Fatalf("first chunk events=%d timing=%+v, want one event without first-token yet", len(first.Events), first.Timing)
	}
	state, err := decodeParserState(domain.UsageSourceRecord{
		Kind:            domain.UsageSourceCodexRollout,
		ParserStateJSON: first.Cursor.ParserStateJSON,
	})
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	second := parseRecordsWithState(source, []jsonlRecord{
		{Offset: 200, Data: []byte(`{"type":"response_item","timestamp":"2026-07-01T10:00:03Z","payload":{"type":"message","role":"assistant","content":[]}}`)},
	}, 300, time.Unix(1700000000, 0).UTC(), state)
	if len(second.Events) != 0 || len(second.Timing) != 1 {
		t.Fatalf("second chunk events=%d timing=%d, want a first-token close", len(second.Events), len(second.Timing))
	}
	closed := second.Timing[0]
	if closed.SourceEventKey != first.Events[0].SourceEventKey ||
		usageTimingValue(closed.FirstTokenMS) != 3000 ||
		usageTimingValue(closed.LLMMS) != 5000 {
		t.Fatalf("closed timing = %+v, want first-token 3000 and llm preserved", closed)
	}
}

// TestCodexTimingFirstResponseBeforeFirstEvent covers the ordering where the
// turn's first assistant message response is read before its first token_count
// usage event: the first event consumes the held first-token directly.
func TestCodexTimingFirstResponseBeforeFirstEvent(t *testing.T) {
	source := usageSource(domain.UsageSourceCodexRollout)
	result := parseRecords(source, []jsonlRecord{
		{Offset: 0, Data: []byte(`{"type":"turn_context","timestamp":"2026-07-01T10:00:00Z","payload":{"model":"gpt-5.6"}}`)},
		{Offset: 100, Data: []byte(`{"type":"response_item","timestamp":"2026-07-01T10:00:03Z","payload":{"type":"message","role":"assistant","content":[]}}`)},
		{Offset: 200, Data: codexTokenLine("2026-07-01T10:00:05Z", 100, 60, 0, 20, 5)},
	}, 300, time.Unix(1700000000, 0).UTC())
	if len(result.Timing) != 1 {
		t.Fatalf("timing = %d, want 1", len(result.Timing))
	}
	got := result.Timing[0]
	if usageTimingValue(got.FirstTokenMS) != 3000 || usageTimingValue(got.LLMMS) != 5000 {
		t.Fatalf("timing = %+v, want first-token 3000 and llm 5000", got)
	}
}

// TestKimiTimingDerivation covers the Kimi wire rules: LLM elapsed is the
// usage.record minus the most recent user message, first-token is that same
// interval when the user message started a round, and tool elapsed stays nil.
func TestKimiTimingDerivation(t *testing.T) {
	source := usageSource(testKimiWireSource)
	source.NativeRootID = "kimi-session"
	records := []jsonlRecord{
		{Offset: 0, Data: []byte(`{"id":"message-1","time":"2026-08-09T09:59:00Z","type":"message.create","message":{"role":"user","content":"hello"}}`)},
		{Offset: 100, Data: []byte(`{"id":"usage-1","time":"2026-08-09T10:00:00Z","type":"usage.record","model":"kimi-for-coding","usage":{"inputOther":13,"inputCacheRead":21,"inputCacheCreation":8,"output":5}}`)},
	}
	result := parseRecords(source, records, 200, time.Unix(1700000000, 0).UTC())
	if len(result.Events) != 1 || len(result.Timing) != 1 {
		t.Fatalf("events=%d timing=%d, want 1/1", len(result.Events), len(result.Timing))
	}
	got := result.Timing[0]
	if got.RoundSeq != 1 || usageTimingValue(got.LLMMS) != 60000 ||
		usageTimingValue(got.FirstTokenMS) != 60000 || got.ToolMS != nil {
		t.Fatalf("timing = %+v", got)
	}
}

func usageTimingValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

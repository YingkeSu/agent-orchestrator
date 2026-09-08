package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// TestApplyUsageChunkWritesTimingAtomically covers the timing ADR's atomic
// write: a chunk that inserts events also inserts their timing rows, and a
// chunk that fails (an event conflict) rolls the timing rows back with it.
func TestApplyUsageChunkWritesTimingAtomically(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	llm := int64(4000)
	firstToken := int64(1500)
	tool := int64(800)
	event := anthropicUsageEvent("msg-1", 10, 0, 0, 5)
	event.CreatedAt = now
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey,
		RoundSeq:       1,
		LLMMS:          &llm,
		ToolMS:         &tool,
		FirstTokenMS:   &firstToken,
	}}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	if len(rows) != 1 {
		t.Fatalf("timing rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.SourceEventKey != event.SourceEventKey || got.RoundSeq != 1 ||
		usageTokenValue(got.LLMMS) != 4000 || usageTokenValue(got.ToolMS) != 800 ||
		usageTokenValue(got.FirstTokenMS) != 1500 {
		t.Fatalf("timing row = %+v", got)
	}
}

// TestApplyUsageChunkTimingRollsBackOnConflict covers atomicity on the failure
// path: an event conflict aborts the whole chunk transaction, so the timing
// row written earlier in the same chunk must not survive.
func TestApplyUsageChunkTimingRollsBackOnConflict(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	conflicting := anthropicUsageEvent("msg-1", 10, 0, 0, 5)
	conflicting.CreatedAt = now
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{conflicting}, nil); err != nil {
		t.Fatalf("apply first chunk: %v", err)
	}

	// Same key with a changed token vector conflicts; the timing entry for it
	// must be rolled back with the failed event write.
	changed := conflicting
	changed.Tokens = canonicalUsageTokens(99, 0, 99, 1)
	llm := int64(4000)
	err := s.ApplyUsageChunk(context.Background(), source.ID, 100, now, domain.SourceCursorState{
		ByteOffset: 200, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now.Add(time.Second),
	}, []domain.ModelUsageEvent{changed}, []domain.UsageEventTiming{{
		SourceEventKey: "msg-1", RoundSeq: 2, LLMMS: &llm,
	}})
	if !errors.Is(err, domain.ErrUsageSourceEventConflict) {
		t.Fatalf("apply conflicting chunk err = %v, want source event conflict", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	// The event itself survives (the LEFT JOIN row), but its timing row must
	// have been rolled back with the failed chunk: RoundSeq 0 and nil durations
	// mean no timing facts were committed.
	if len(rows) != 1 {
		t.Fatalf("timing rows after rollback = %d, want the event row only", len(rows))
	}
	if rows[0].SourceEventKey != "msg-1" || rows[0].RoundSeq != 0 || rows[0].LLMMS != nil {
		t.Fatalf("timing row after rollback = %+v, want the conflict's timing rolled back", rows[0])
	}
}

// TestApplyUsageChunkTimingReplayUpserts covers the ADR's replacement
// re-derivation: a replay of the same logical event under a new generation
// refreshes the timing row in place rather than writing a duplicate.
func TestApplyUsageChunkTimingReplayUpserts(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	llm := int64(4000)
	event := anthropicUsageEvent("msg-1", 10, 0, 0, 5)
	event.CreatedAt = now
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey, RoundSeq: 1, LLMMS: &llm,
	}}); err != nil {
		t.Fatalf("apply first chunk: %v", err)
	}

	// The replacement generation re-derives the same event with new timing.
	newLLM := int64(9000)
	replay := event
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 100, now, domain.SourceCursorState{
		ByteOffset: 200, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now.Add(time.Second),
	}, []domain.ModelUsageEvent{replay}, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey, RoundSeq: 2, LLMMS: &newLLM,
	}}); err != nil {
		t.Fatalf("apply replay chunk: %v", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	if len(rows) != 1 {
		t.Fatalf("timing rows = %d, want 1 (upsert, not duplicate)", len(rows))
	}
	if rows[0].RoundSeq != 2 || usageTokenValue(rows[0].LLMMS) != 9000 {
		t.Fatalf("replayed timing row = %+v, want refreshed round/llm", rows[0])
	}
}

// TestApplyUsageChunkTimingCrossChunkClose covers a tool elapsed (Claude) or
// first-token (Codex) whose closing record arrives in a later chunk: the
// timing entry references the already-committed event and refreshes only the
// facts it carries.
func TestApplyUsageChunkTimingCrossChunkClose(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	llm := int64(4000)
	event := anthropicUsageEvent("msg-1", 10, 0, 0, 5)
	event.CreatedAt = now
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey, RoundSeq: 1, LLMMS: &llm,
	}}); err != nil {
		t.Fatalf("apply first chunk: %v", err)
	}

	// The tool_result record arrives in a later chunk: no new events, only a
	// timing refresh for the committed event.
	tool := int64(2500)
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 100, now, domain.SourceCursorState{
		ByteOffset: 200, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now.Add(time.Second),
	}, nil, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey, RoundSeq: 1, LLMMS: &llm, ToolMS: &tool,
	}}); err != nil {
		t.Fatalf("apply close chunk: %v", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	if len(rows) != 1 {
		t.Fatalf("timing rows = %d, want 1", len(rows))
	}
	if usageTokenValue(rows[0].LLMMS) != 4000 || usageTokenValue(rows[0].ToolMS) != 2500 {
		t.Fatalf("closed timing row = %+v, want llm preserved and tool set", rows[0])
	}
}

// TestUsageEventTimingCascadesOnDelete covers the timing ADR's retention rule:
// the child row lives and dies with its usage event, so deleting the session
// removes the timing row through the existing cascade chain.
func TestUsageEventTimingCascadesOnDelete(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	llm := int64(4000)
	event := anthropicUsageEvent("msg-1", 10, 0, 0, 5)
	event.CreatedAt = now
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}, []domain.UsageEventTiming{{
		SourceEventKey: event.SourceEventKey, RoundSeq: 1, LLMMS: &llm,
	}}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	// Delete the session directly at the SQLite level; the FK chain
	// usage_bindings -> usage_sources/model_usage_events -> timing must carry
	// the timing row away with it.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=foreign_keys(1)")
	mustNoError(t, err)
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DELETE FROM change_log WHERE session_id = ?`, string(session.ID)); err != nil {
		t.Fatalf("clear change log: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id = ?`, string(session.ID)); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	if len(rows) != 0 {
		t.Fatalf("timing rows after session delete = %d, want 0 (cascade)", len(rows))
	}
}

// TestListModelUsageEventTimingNilSemantics covers the unknown-is-nil rule: an
// event without a timing row returns zero durations with RoundSeq 0, and a
// stored NULL duration stays nil rather than becoming a zero.
func TestListModelUsageEventTimingNilSemantics(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessClaudeCode)
	source := seedUsageSource(t, s, session, now)

	llm := int64(4000)
	withTiming := anthropicUsageEvent("msg-timed", 10, 0, 0, 5)
	withTiming.CreatedAt = now
	noTiming := anthropicUsageEvent("msg-plain", 10, 0, 0, 5)
	noTiming.CreatedAt = now.Add(time.Minute)
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{withTiming, noTiming}, []domain.UsageEventTiming{{
		SourceEventKey: withTiming.SourceEventKey, RoundSeq: 1, LLMMS: &llm,
	}}); err != nil {
		t.Fatalf("apply chunk: %v", err)
	}

	rows, err := s.ListModelUsageEventTiming(context.Background(), nil, nil)
	mustNoError(t, err)
	if len(rows) != 2 {
		t.Fatalf("timing rows = %d, want 2", len(rows))
	}
	var plain, timed domain.UsageEventTimingRow
	for _, row := range rows {
		if row.SourceEventKey == noTiming.SourceEventKey {
			plain = row
		} else {
			timed = row
		}
	}
	if plain.RoundSeq != 0 || plain.LLMMS != nil || plain.ToolMS != nil || plain.FirstTokenMS != nil {
		t.Fatalf("plain row = %+v, want zero round and nil durations", plain)
	}
	if timed.RoundSeq != 1 || usageTokenValue(timed.LLMMS) != 4000 || timed.ToolMS != nil || timed.FirstTokenMS != nil {
		t.Fatalf("timed row = %+v, want llm only", timed)
	}
}

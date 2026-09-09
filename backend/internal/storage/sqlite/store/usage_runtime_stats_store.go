package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// terminalRuntimeActivityStatuses are the activity statuses whose elapsed time
// counts as tool time (timing ADR Decision 1: a tool call's elapsed is its
// terminal updated_at minus its created_at; non-terminal activities are not
// countable yet, so a session whose tool calls are all still running reports
// unknown).
var terminalRuntimeActivityStatuses = map[domain.ActivityStatus]bool{
	domain.ActivityStatusCompleted: true,
	domain.ActivityStatusFailed:    true,
	domain.ActivityStatusCancelled: true,
	domain.ActivityStatusRecovered: true,
	domain.ActivityStatusResolved:  true,
}

// promptBearingMessageOrigins are the origins whose user message marks a
// prompt-bearing turn (a round). Daemon-only turns carry no such message.
var promptBearingMessageOrigins = map[domain.MessageOrigin]bool{
	domain.MessageOriginHuman:      true,
	domain.MessageOriginAutomation: true,
}

// AggregateSessionRuntimeTiming returns the native/TUI-mode runtime rollup for
// one session (timing ADR #9), computed at read time from certified usage
// events and their timing rows. The SQL exposes -1 for every rollup that has no
// known value (SUM over no timing rows); this maps each to nil so the service
// boundary keeps the unknown-is-nil rule.
func (s *Store) AggregateSessionRuntimeTiming(ctx context.Context, sessionID domain.SessionID) (domain.SessionRuntimeTimingAggregate, error) {
	row, err := s.qr.AggregateSessionRuntimeTiming(ctx, sessionID)
	if err != nil {
		return domain.SessionRuntimeTimingAggregate{}, fmt.Errorf("aggregate session runtime timing for %s: %w", sessionID, err)
	}
	return domain.SessionRuntimeTimingAggregate{
		StepCount:        row.StepCount,
		TimingRowCount:   row.TimingRowCount,
		RoundCount:       row.RoundCount,
		LLMMSTotal:       sentinelInt64Ptr(row.LlmMsTotal),
		ToolMSTotal:      sentinelInt64Ptr(row.ToolMsTotal),
		FirstTokenMSSum:  sentinelInt64Ptr(row.FirstTokenMsSum),
		FirstTokenKnown:  row.FirstTokenKnown,
		RateOutputTokens: sentinelInt64Ptr(row.RateOutputTokens),
		RateLLMMS:        sentinelInt64Ptr(row.RateLlmMs),
	}, nil
}

// ListConversationRuntimeTurnFacts returns one row per conversation turn on
// the session's active branch lineage with the per-turn facts the chat-mode
// stats derivation needs (timing ADR #9): the prompt-bearing flag, the
// assistant step count, the first-token delta measured from requested_at, and
// the terminal tool elapsed. The per-turn facts aggregate the unified
// content-row read in Go because the driver's stored timestamp text is not
// parseable by SQLite's date functions. A session with no conversation returns
// an empty slice.
func (s *Store) ListConversationRuntimeTurnFacts(ctx context.Context, sessionID domain.SessionID) ([]domain.ConversationRuntimeTurnFact, error) {
	turns, err := s.qr.ListConversationRuntimeTurnFacts(ctx, &sessionID)
	if err != nil {
		return nil, fmt.Errorf("list conversation runtime turn facts for %s: %w", sessionID, err)
	}
	content, err := s.qr.ListConversationRuntimeContentRows(ctx, &sessionID)
	if err != nil {
		return nil, fmt.Errorf("list conversation runtime content rows for %s: %w", sessionID, err)
	}

	type chatTurnFacts struct {
		promptBearing   bool
		toolKnown       bool
		toolMS          int64
		assistantCount  int64
		firstContent    time.Time
		hasFirstContent bool
	}
	facts := make(map[string]*chatTurnFacts, len(turns))
	for _, turn := range turns {
		facts[turn.TurnID] = &chatTurnFacts{}
	}

	for _, row := range content {
		if !row.TurnID.Valid {
			// Items the provider never attributed to a turn cannot count
			// toward any turn's work.
			continue
		}
		fact, ok := facts[row.TurnID.String]
		if !ok {
			// Content of a discarded (rolled-back, promoted, cancelled) turn.
			continue
		}
		// The earliest assistant content row anchors the first-token interval
		// (ADR Decision 3); the chat driver stamps assistant message rows when
		// the first streaming delta arrives. User prompts never anchor it.
		anchorsFirstToken := false
		switch row.RowKind {
		case "message":
			switch domain.MessageRole(row.Role) {
			case domain.MessageRoleAssistant:
				fact.assistantCount++
				anchorsFirstToken = true
			case domain.MessageRoleUser:
				if promptBearingMessageOrigins[domain.MessageOrigin(row.Origin)] {
					fact.promptBearing = true
				}
			}
		case "activity":
			anchorsFirstToken = true
			if terminalRuntimeActivityStatuses[domain.ActivityStatus(row.Status)] {
				fact.toolKnown = true
				fact.toolMS += row.UpdatedAt.Sub(row.CreatedAt).Milliseconds()
			}
		}
		if anchorsFirstToken && (!fact.hasFirstContent || row.CreatedAt.Before(fact.firstContent)) {
			fact.firstContent = row.CreatedAt
			fact.hasFirstContent = true
		}
	}

	out := make([]domain.ConversationRuntimeTurnFact, 0, len(turns))
	for _, turn := range turns {
		fact := facts[turn.TurnID]
		item := domain.ConversationRuntimeTurnFact{
			TurnID:         turn.TurnID,
			State:          turn.State,
			RequestedAt:    turn.RequestedAt,
			StartedAt:      nullTimePtr(turn.StartedAt),
			CompletedAt:    nullTimePtr(turn.CompletedAt),
			ToolMS:         fact.toolMS,
			ToolKnown:      fact.toolKnown,
			AssistantCount: fact.assistantCount,
			PromptBearing:  fact.promptBearing,
		}
		// A first-content row that predates the request (clock skew) is not a
		// certifiable interval and stays unknown, never negative.
		if fact.hasFirstContent && !fact.firstContent.Before(turn.RequestedAt) {
			delta := fact.firstContent.Sub(turn.RequestedAt).Milliseconds()
			item.FirstTokenDeltaMS = &delta
		}
		out = append(out, item)
	}
	return out, nil
}

// sentinelInt64Ptr maps a -1 sentinel (the SQL's "no known value" marker for a
// COALESCE'd rollup) to nil, preserving the unknown-is-nil rule across the
// store boundary. Real values are never negative.
func sentinelInt64Ptr(value int64) *int64 {
	if value < 0 {
		return nil
	}
	return &value
}

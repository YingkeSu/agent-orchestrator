package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// The certifier's whole job is deciding which archived chat events are usage
// facts and which vocabulary they speak. These tests drive the real store so
// the storage CHECKs, dedupe keys, and cursor CAS are exercised exactly as the
// daemon would.

func seedACPConversation(
	t *testing.T,
	store *sqlite.Store,
	session domain.SessionRecord,
	conversationID string,
) string {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	_, err := store.CreateConversation(ctx, conversationID, domain.ConversationScopeSession,
		session.ProjectID, session.ID, now)
	mustNoError(t, err)
	return conversationID
}

func seedACPUsageEvent(
	t *testing.T,
	store *sqlite.Store,
	session domain.SessionRecord,
	generation string,
	conversationID string,
	usage map[string]any,
	receivedAt time.Time,
) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"kind":            "usage",
		"providerEventId": "",
		"providerTurnId":  "",
		"usage":           usage,
		"rateLimits":      nil,
		"controllerState": "",
	})
	mustNoError(t, err)
	projected, err := store.ProjectProviderEvent(context.Background(), conversationID, session.ID,
		generation, "", "usage", string(payload), receivedAt,
		func(ctx context.Context) error { return nil })
	mustNoError(t, err)
	if !projected {
		t.Fatalf("provider event for %s was not projected", conversationID)
	}
}

func sessionControllerGeneration(t *testing.T, dataDir string, sessionID domain.SessionID) string {
	t.Helper()
	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	var generation string
	mustNoError(t, db.QueryRow(`SELECT controller_generation FROM sessions WHERE id = ?`, sessionID).Scan(&generation),
		"read controller generation")
	return generation
}

func acpTurnUsage(input, output, cached int64) map[string]any {
	return map[string]any{
		"InputTokens": input, "OutputTokens": output, "CachedTokens": cached,
		"TotalTokens": input + output + cached, "ContextUsed": int64(0), "ContextWindow": int64(0),
		"ContextKnown": false, "TotalsKnown": true, "Cost": nil, "Currency": "",
	}
}

func acpHeartbeatUsage(contextUsed, contextWindow int64) map[string]any {
	return map[string]any{
		"InputTokens": int64(0), "OutputTokens": int64(0), "CachedTokens": int64(0),
		"TotalTokens": int64(0), "ContextUsed": contextUsed, "ContextWindow": contextWindow,
		"ContextKnown": true, "TotalsKnown": false, "Cost": nil, "Currency": "",
	}
}

func setConversationUsageModel(t *testing.T, dataDir string, conversationID, model string) {
	t.Helper()
	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	_, err := db.Exec(`UPDATE conversations SET model = ? WHERE id = ?`, model, conversationID)
	mustNoError(t, err)
}

func setSessionTerminated(t *testing.T, dataDir string, sessionID domain.SessionID) {
	t.Helper()
	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	_, err := db.Exec(`UPDATE sessions SET is_terminated = 1 WHERE id = ?`, sessionID)
	mustNoError(t, err)
}

func openACPTestDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	return db
}

func countACPUsageEvents(t *testing.T, dataDir string, where string, args ...any) int64 {
	t.Helper()
	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	var count int64
	mustNoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM model_usage_events mue
		 JOIN usage_sources us ON us.id = mue.usage_source_id
		 WHERE us.kind = 'acp_usage' AND `+where, args...).Scan(&count), "count acp events")
	return count
}

func TestACPCertifierCertifiesTurnUsageAndSkipsHeartbeats(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-1")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	received := now
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpHeartbeatUsage(27003, 1000000), received)
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(467, 417, 293760), received.Add(time.Minute))
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(381, 691, 99840), received.Add(2*time.Minute))

	certifier := NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }})
	certifier.Sync(ctx)

	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 2 {
		t.Fatalf("certified event count = %d, want 2 (heartbeat skipped)", got)
	}
	binding, ok, err := store.GetUsageBinding(ctx, session.ID, domain.HarnessOpenCode, conversationID)
	mustNoError(t, err)
	if !ok {
		t.Fatalf("no usage binding created for conversation %s", conversationID)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	mustNoError(t, err)
	if len(sources) != 1 || sources[0].Kind != domain.UsageSourceACPUsage ||
		sources[0].ArtifactPath != acpArtifactPath(conversationID) {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].ByteOffset == 0 {
		t.Fatalf("cursor did not advance past the heartbeat")
	}

	events, err := store.ListUsageRequestLog(ctx, nil, nil, string(domain.UsageSourceACPUsage), "", nil, 10)
	mustNoError(t, err)
	if len(events) != 2 {
		t.Fatalf("request log = %+v", events)
	}
	first := events[0]
	if first.SourceKind != domain.UsageSourceACPUsage || first.ModelID != "deepseek-v4-flash" ||
		first.SessionID != session.ID {
		t.Fatalf("request log entry = %+v", first)
	}
	if first.LLMMS != nil || first.FirstTokenMS != nil {
		t.Fatalf("ACP events must carry no timing facts, got %+v", first)
	}
	// Newest first: the second turn. Input folds the cache bucket; the
	// read/write split is unknown, so cached stays nil, never zero.
	if got := *first.InputTokens; got != 381+99840 {
		t.Fatalf("input tokens = %d, want %d", got, 381+99840)
	}
	if got := *first.OutputTokens; got != 691 {
		t.Fatalf("output tokens = %d, want 691", got)
	}
	if first.CachedInputTokens != nil {
		t.Fatalf("cached tokens = %v, want nil (unknown split)", *first.CachedInputTokens)
	}
	second := events[1]
	if got := *second.InputTokens; got != 467+293760 {
		t.Fatalf("input tokens = %d, want %d", got, 467+293760)
	}
	if second.CreatedAt == nil || !second.CreatedAt.Equal(received.Add(time.Minute)) {
		t.Fatalf("created_at = %v, want receive timestamp %v", second.CreatedAt, received.Add(time.Minute))
	}
}

func TestACPCertifierRecordsKnownZeroTurn(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-zero")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(0, 0, 0), now)

	NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }}).Sync(ctx)

	events, err := store.ListUsageRequestLog(ctx, nil, nil, string(domain.UsageSourceACPUsage), "", nil, 10)
	mustNoError(t, err)
	if len(events) != 1 {
		t.Fatalf("known-zero turn = %+v, want exactly one recorded event", events)
	}
	entry := events[0]
	if entry.InputTokens == nil || *entry.InputTokens != 0 ||
		entry.OutputTokens == nil || *entry.OutputTokens != 0 {
		t.Fatalf("known-zero turn = %+v, want stored known zeros", entry)
	}
}

func TestACPCertifierReplayIsIdempotentAndCatchesUp(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-replay")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	received := now
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(100, 50, 900), received)
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(200, 60, 800), received.Add(time.Minute))

	certifier := NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }})
	certifier.Sync(ctx)
	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 2 {
		t.Fatalf("first pass = %d events, want 2", got)
	}
	certifier.Sync(ctx)
	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 2 {
		t.Fatalf("re-scan duplicated events: %d, want 2", got)
	}

	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(300, 70, 700), received.Add(2*time.Minute))
	certifier.Sync(ctx)
	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 3 {
		t.Fatalf("catch-up pass = %d events, want 3", got)
	}
}

func TestACPCertifierPricesCataloguedModelAndLeavesUnknownNil(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	pricedConv := seedACPConversation(t, store, session, "conv-acp-priced")
	setConversationUsageModel(t, dataDir, pricedConv, "gpt-test")
	secondSession, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: session.ProjectID,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessOpenCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)
	unknownConv := seedACPConversation(t, store, secondSession, "conv-acp-unknown")
	setConversationUsageModel(t, dataDir, unknownConv, "deepseek-not-in-catalog")
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), pricedConv, acpTurnUsage(1000, 500, 2000), now)
	seedACPUsageEvent(t, store, secondSession, sessionControllerGeneration(t, dataDir, secondSession.ID), unknownConv, acpTurnUsage(1000, 500, 2000), now)

	snapshot := testPricingSnapshot(t, "0.000001")
	NewACPCertifier(store, ACPCertifierConfig{
		Pricing: pricing.NewManager(snapshot),
		Clock:   func() time.Time { return now },
	}).Sync(ctx)

	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	type pricedRow struct {
		modelID, billingProvider, billingSource string
		cost                                    sql.NullInt64
	}
	raw, err := db.Query(`
		SELECT mue.model_id, COALESCE(mue.billing_provider_id, ''),
		       COALESCE(mue.billing_provider_source, ''), mue.estimated_cost_nanos
		FROM model_usage_events mue
		JOIN usage_sources us ON us.id = mue.usage_source_id
		WHERE us.kind = 'acp_usage'
		ORDER BY mue.model_id`)
	mustNoError(t, err)
	defer func() { _ = raw.Close() }()
	byModel := map[string]pricedRow{}
	for raw.Next() {
		var row pricedRow
		mustNoError(t, raw.Scan(&row.modelID, &row.billingProvider, &row.billingSource, &row.cost))
		byModel[row.modelID] = row
	}
	mustNoError(t, raw.Err())
	if len(byModel) != 2 {
		t.Fatalf("rows = %+v, want 2", byModel)
	}
	gpt := byModel["gpt-test"]
	if gpt.billingProvider != "openai" || gpt.billingSource != string(domain.UsageBillingProviderInferred) {
		t.Fatalf("gpt-test attribution = %+v, want openai inferred", gpt)
	}
	// Output is the only fully known component; the folded cache bucket keeps
	// the input components unknown, so the total stays nil.
	if gpt.cost.Valid {
		t.Fatalf("gpt-test total cost = %v, want nil (input split unknown)", gpt.cost.Int64)
	}
	unknown := byModel["deepseek-not-in-catalog"]
	if unknown.billingProvider != "" || unknown.cost.Valid {
		t.Fatalf("uncatalogued model = %+v, want no attribution and nil cost", unknown)
	}
}

func TestACPCertifierSettlesTerminatedSession(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-done")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(10, 5, 20), now)
	setSessionTerminated(t, dataDir, session.ID)

	NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }}).Sync(ctx)

	binding, ok, err := store.GetUsageBinding(ctx, session.ID, domain.HarnessOpenCode, conversationID)
	mustNoError(t, err)
	if !ok || binding.State != domain.UsageBindingComplete {
		t.Fatalf("binding = %+v ok=%v, want complete", binding, ok)
	}
	sources, err := store.ListUsageSourcesForBinding(ctx, binding.ID)
	mustNoError(t, err)
	if len(sources) != 1 || sources[0].State != domain.UsageSourceComplete {
		t.Fatalf("sources = %+v, want one complete acp_usage source", sources)
	}
}

func TestACPCertifierKeepsCompletedSessionClosedAcrossRescans(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-closed")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(10, 5, 20), now)
	setSessionTerminated(t, dataDir, session.ID)

	certifier := NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }})
	certifier.Sync(ctx)
	certifier.Sync(ctx)

	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

func TestACPCertifierStoresVerbatimProviderUsageObject(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-verbatim")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID), conversationID, acpTurnUsage(12, 34, 56), now)

	NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }}).Sync(ctx)

	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	var providerUsage, key string
	mustNoError(t, db.QueryRow(`
		SELECT mue.provider_usage_json, mue.source_event_key
		FROM model_usage_events mue
		JOIN usage_sources us ON us.id = mue.usage_source_id
		WHERE us.kind = 'acp_usage'`).Scan(&providerUsage, &key))
	var object map[string]any
	mustNoError(t, json.Unmarshal([]byte(providerUsage), &object))
	for _, field := range []string{"InputTokens", "OutputTokens", "CachedTokens", "TotalTokens", "TotalsKnown"} {
		if _, ok := object[field]; !ok {
			t.Fatalf("provider usage object missing %q: %s", field, providerUsage)
		}
	}
	if key == "" {
		t.Fatal("empty source event key")
	}
}

func TestCertifyACPEventRejectsPayloadWithoutUsageObject(t *testing.T) {
	// The conversation scan only lists archived kind="usage" rows, but a
	// projected payload can be malformed. Pin that it certifies nothing
	// instead of poisoning the chunk.
	now := time.Unix(1700000000, 0).UTC()
	if _, ok := certifyACPEvent(domain.ACPUsageEvent{
		ID:          7,
		PayloadJSON: `{"kind":"usage"}`,
		ReceivedAt:  now,
	}, "any-model"); ok {
		t.Fatal("payload without a usage object must not certify")
	}
	if _, ok := certifyACPEvent(domain.ACPUsageEvent{
		ID:          8,
		PayloadJSON: `not json`,
		ReceivedAt:  now,
	}, "any-model"); ok {
		t.Fatal("malformed payload must not certify")
	}
}

func clearConversationUsageModel(t *testing.T, dataDir string, conversationID string) {
	t.Helper()
	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	_, err := db.Exec(`UPDATE conversations SET model = NULL WHERE id = ?`, conversationID)
	mustNoError(t, err)
}

// A rebound conversation keeps one archive but accumulates rows stamped with
// each controlling session. Each session's binding must certify only its own
// rows: the binding is (session_id, harness, native_root_id), and a page that
// leaked the other session's rows would re-derive their acp:<row_id> keys
// under a second binding_id and double-count every pre-rebind event.
func TestACPCertifierDoesNotCrossCertifyReboundConversation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-rebind")
	setConversationUsageModel(t, dataDir, conversationID, "deepseek-v4-flash")
	rebound, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: session.ProjectID,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessOpenCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	})
	mustNoError(t, err)

	// Pre-rebind turn archived under the original session, post-rebind turn
	// under the replacement session; distinct token counts identify them.
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID),
		conversationID, acpTurnUsage(111, 22, 33), now)
	seedACPUsageEvent(t, store, rebound, sessionControllerGeneration(t, dataDir, rebound.ID),
		conversationID, acpTurnUsage(444, 55, 66), now.Add(time.Minute))

	certifier := NewACPCertifier(store, ACPCertifierConfig{Clock: func() time.Time { return now }})
	certifier.Sync(ctx)

	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	type perSession struct {
		events int64
		input  int64
	}
	rows, err := db.Query(`
		SELECT ub.session_id, CAST(COUNT(*) AS INTEGER), CAST(COALESCE(SUM(mue.input_tokens), 0) AS INTEGER)
		FROM model_usage_events mue
		JOIN usage_bindings ub ON ub.id = mue.binding_id
		WHERE ub.session_id IN (?, ?)
		GROUP BY ub.session_id`, session.ID, rebound.ID)
	mustNoError(t, err)
	defer func() { _ = rows.Close() }()
	bySession := map[domain.SessionID]perSession{}
	for rows.Next() {
		var sessionID domain.SessionID
		var counts perSession
		mustNoError(t, rows.Scan(&sessionID, &counts.events, &counts.input))
		bySession[sessionID] = counts
	}
	mustNoError(t, rows.Err())
	if got := bySession[session.ID]; got.events != 1 || got.input != 111+33 {
		t.Fatalf("original session certified %+v, want exactly its own turn (input %d)", got, 111+33)
	}
	if got := bySession[rebound.ID]; got.events != 1 || got.input != 444+66 {
		t.Fatalf("rebound session certified %+v, want exactly its own turn (input %d)", got, 444+66)
	}

	// The whole point of the issue: re-scans stay duplicate-free even with
	// two bindings watching one conversation.
	certifier.Sync(ctx)
	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 2 {
		t.Fatalf("re-scan produced %d events, want 2", got)
	}
}

// conversations.model is legitimately NULL (the provider default answered, or
// an agent switch reset the overrides). The documented "unknown" fallback must
// keep those events certifiable — valid model id, no attribution, nil cost —
// instead of wedging the conversation in a permanent retry loop.
func TestACPCertifierFallsBackToUnknownModelWhenUnset(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	dataDir := t.TempDir()
	store, session := seedUsageTestSession(t, dataDir, "usage", domain.HarnessOpenCode, domain.ActivityIdle, "", now)
	conversationID := seedACPConversation(t, store, session, "conv-acp-unset-model")
	clearConversationUsageModel(t, dataDir, conversationID)
	seedACPUsageEvent(t, store, session, sessionControllerGeneration(t, dataDir, session.ID),
		conversationID, acpTurnUsage(90, 40, 160), now)

	snapshot := testPricingSnapshot(t, "0.000001")
	certifier := NewACPCertifier(store, ACPCertifierConfig{
		Pricing: pricing.NewManager(snapshot),
		Clock:   func() time.Time { return now },
	})
	certifier.Sync(ctx)

	db := openACPTestDB(t, dataDir)
	defer func() { _ = db.Close() }()
	var modelID, billingProvider string
	var cost sql.NullInt64
	mustNoError(t, db.QueryRow(`
		SELECT mue.model_id, COALESCE(mue.billing_provider_id, ''), mue.estimated_cost_nanos
		FROM model_usage_events mue
		JOIN usage_sources us ON us.id = mue.usage_source_id
		WHERE us.kind = 'acp_usage'`).Scan(&modelID, &billingProvider, &cost))
	if modelID != "unknown" {
		t.Fatalf("unset model certified as %q, want the unknown sentinel", modelID)
	}
	if billingProvider != "" || cost.Valid {
		t.Fatalf("unset model attribution = %q cost valid = %v, want unattributed nil cost", billingProvider, cost.Valid)
	}

	certifier.Sync(ctx)
	if got := countACPUsageEvents(t, dataDir, "1=1"); got != 1 {
		t.Fatalf("re-scan produced %d events, want 1", got)
	}
}

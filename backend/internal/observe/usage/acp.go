package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

const (
	// defaultACPCertifyInterval paces the durable-archive re-scan. Worker
	// usage statistics tolerate this latency; the archive is AO state, so a
	// missed tick only delays certification, never loses it.
	defaultACPCertifyInterval = 30 * time.Second
	// acpChunkEvents bounds one ApplyUsageChunk transaction.
	acpChunkEvents = 256
)

// acpArtifactPath is the stable source identity for one conversation's usage
// archive. It is not a filesystem path: acp_usage sources are table-backed,
// and the transcript watcher never sees them.
func acpArtifactPath(conversationID string) string {
	return "acp:" + conversationID
}

// acpSourceEventKey derives the replay identity of one certified event from
// the archived provider-event row id. Re-scanning the archive re-derives the
// same key, so the events table's UNIQUE(binding_id, source_event_key) makes
// every pass idempotent.
func acpSourceEventKey(rowID int64) string {
	return fmt.Sprintf("acp:%d", rowID)
}

type acpCertifierStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	UpsertUsageBinding(context.Context, domain.UsageBindingRecord) (domain.UsageBindingRecord, error)
	UpdateUsageBindingState(context.Context, int64, domain.UsageBindingState, string, time.Time) (bool, error)
	CompleteUsageBindingIfSettled(context.Context, int64, time.Time) (bool, error)
	ListUsageSourcesForBinding(context.Context, int64) ([]domain.UsageSourceRecord, error)
	InsertUsageSource(context.Context, domain.UsageSourceRecord) (domain.UsageSourceRecord, error)
	ReactivateUsageSource(context.Context, int64, time.Time) (bool, error)
	MarkUsageSourceState(context.Context, int64, domain.UsageSourceState, string, *time.Time, time.Time) (bool, error)
	GetUsageSourceForIngestion(context.Context, int64) (domain.UsageSourceContext, bool, error)
	ApplyUsageChunk(context.Context, int64, int64, time.Time, domain.SourceCursorState, []domain.ModelUsageEvent, []domain.UsageEventTiming) error
	ListACPUsageEventConversations(context.Context) ([]domain.ACPUsageConversationRef, error)
	ListACPUsageEventsAfter(context.Context, string, domain.SessionID, int64, int64) ([]domain.ACPUsageEvent, error)
	ConversationUsageModel(context.Context, string) (string, bool, error)
}

// ACPCertifierConfig configures the chat-provider usage certifier.
type ACPCertifierConfig struct {
	Pricing        *pricing.Manager
	OnPricingError func(error)
	Clock          func() time.Time
	Interval       time.Duration
	Logger         *slog.Logger
}

// ACPCertifier ingests kind="usage" conversation_provider_events into the
// certified usage pipeline as acp_usage sources.
//
// Worker sessions driven through a chat provider have no provider-owned
// transcript to certify; their token accounting arrives as archived provider
// events instead. Each TotalsKnown payload certifies one per-turn usage fact:
//
//   - Zero-token heartbeats (context-occupancy updates, TotalsKnown=false) are
//     skipped: an omitted token-accounting group is unknown, not a known zero,
//     and recording one would fabricate a zero-token request. A TotalsKnown
//     turn whose counters are all zero IS recorded — that is a real, known
//     zero (an empty turn still happened).
//   - The cache bucket folds cache reads and writes into one counter, and the
//     read/write split is unrecoverable, so the canonical vector records
//     input = input + folded cache (the total non-output tokens, a fact) and
//     leaves cached/uncached nil: unknown is nil, never a guessed split.
//   - Timing fields stay NULL. The archive carries no transcript anchors, so
//     there are no certified durations to derive (ADR 0005's rule for
//     uncertified boundaries); created_at is the durable receive timestamp.
type ACPCertifier struct {
	store          acpCertifierStore
	pricing        *pricing.Manager
	onPricingError func(error)
	now            func() time.Time
	interval       time.Duration
	logger         *slog.Logger
	mu             sync.Mutex
}

// NewACPCertifier constructs a chat-provider usage certifier.
func NewACPCertifier(store acpCertifierStore, cfg ACPCertifierConfig) *ACPCertifier {
	if cfg.OnPricingError == nil {
		cfg.OnPricingError = func(error) {}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultACPCertifyInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &ACPCertifier{
		store:          store,
		pricing:        cfg.Pricing,
		onPricingError: cfg.OnPricingError,
		now:            cfg.Clock,
		interval:       cfg.Interval,
		logger:         cfg.Logger,
	}
}

// Start runs one backfill pass immediately, then re-scans on the ticker until
// ctx is canceled. The returned channel closes after the final pass.
func (c *ACPCertifier) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			c.Sync(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// Sync runs one bounded certification pass over the whole archive. Passes are
// single-flight: an overlapping ticker tick is a no-op, and concurrent passes
// could race the same source cursor.
func (c *ACPCertifier) Sync(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	if !c.mu.TryLock() {
		return
	}
	defer c.mu.Unlock()
	refs, err := c.store.ListACPUsageEventConversations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Warn("list ACP usage conversations failed", "err", err)
		}
		return
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := c.syncConversation(ctx, ref); err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("certify ACP usage failed", "conversation", ref.ConversationID, "err", err)
			}
		}
	}
}

func (c *ACPCertifier) syncConversation(ctx context.Context, ref domain.ACPUsageConversationRef) error {
	session, ok, err := c.store.GetSession(ctx, ref.SessionID)
	if err != nil || !ok {
		return err
	}
	// These harnesses already have certified transcript sources. Codex also
	// emits cumulative counters, which cannot be summed as ACP per-turn facts.
	switch session.Harness {
	case domain.HarnessClaudeCode, domain.HarnessCodex, domain.HarnessKimi:
		return nil
	}
	now := c.now().UTC()
	binding, err := c.ensureBinding(ctx, session, ref.ConversationID, now)
	if err != nil {
		return err
	}
	source, err := c.ensureSource(ctx, binding.ID, ref.ConversationID, now)
	if err != nil {
		return err
	}
	if !session.IsTerminated && binding.State == domain.UsageBindingFinalizing {
		if _, err := c.store.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingActive, "", now); err != nil {
			return err
		}
	}
	modelID, ok, err := c.store.ConversationUsageModel(ctx, ref.ConversationID)
	if err != nil {
		return err
	}
	if !ok {
		// The conversation row is gone, so its archived events are gone too;
		// the conversation scan was a moment stale.
		return nil
	}
	// conversations.model is legitimately NULL: it means the provider default
	// answered and nothing durable names which model that was. Certify under
	// the same "unknown" sentinel the transcript parsers use — the event stays
	// valid, and no catalog lists the sentinel, so attribution and cost stay
	// nil instead of inventing a model or wedging this conversation in a
	// permanent retry loop.
	modelID = firstNonEmpty(modelID, "unknown")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sourceCtx, ok, err := c.store.GetUsageSourceForIngestion(ctx, source.ID)
		if err != nil || !ok {
			return err
		}
		source = sourceCtx.Source
		archive, err := c.store.ListACPUsageEventsAfter(ctx, ref.ConversationID, ref.SessionID, sourceCtx.Source.ByteOffset, acpChunkEvents)
		if err != nil {
			return err
		}
		if len(archive) == 0 {
			return c.settleConversation(ctx, session.IsTerminated, sourceCtx, now)
		}
		if sourceCtx.Source.State == domain.UsageSourceComplete {
			if session.IsTerminated {
				break
			}
			if _, err := c.store.ReactivateUsageSource(ctx, sourceCtx.Source.ID, now); err != nil {
				return err
			}
			sourceCtx.Source.State = domain.UsageSourceActive
		}
		events, lastID := certifyACPEvents(archive, modelID)
		retired, err := c.applyCertifiedChunk(ctx, sourceCtx, events, lastID, now)
		if err != nil || retired {
			return err
		}
		if len(archive) < acpChunkEvents {
			return c.settleConversation(ctx, session.IsTerminated, sourceCtx, now)
		}
	}
	return nil
}

func (c *ACPCertifier) ensureBinding(
	ctx context.Context,
	session domain.SessionRecord,
	conversationID string,
	now time.Time,
) (domain.UsageBindingRecord, error) {
	return c.store.UpsertUsageBinding(ctx, domain.UsageBindingRecord{
		SessionID:    session.ID,
		Harness:      session.Harness,
		NativeRootID: conversationID,
		State:        domain.UsageBindingActive,
		UpdatedAt:    now,
	})
}

func (c *ACPCertifier) ensureSource(ctx context.Context, bindingID int64, conversationID string, now time.Time) (domain.UsageSourceRecord, error) {
	sources, err := c.store.ListUsageSourcesForBinding(ctx, bindingID)
	if err != nil {
		return domain.UsageSourceRecord{}, err
	}
	path := acpArtifactPath(conversationID)
	for _, source := range sources {
		if source.Kind == domain.UsageSourceACPUsage && source.ArtifactPath == path {
			return source, nil
		}
	}
	return c.store.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:    bindingID,
		Kind:         domain.UsageSourceACPUsage,
		ArtifactPath: path,
		State:        domain.UsageSourceActive,
		UpdatedAt:    now,
	})
}

// applyCertifiedChunk prices the chunk inside the pricing fence and commits it
// with the cursor advance. A replay conflict means the archive no longer
// re-derives what the event stored (a pricing-catalog model inference moved on)
// and can never agree again; the source retires with the conflict code, the
// same terminal state the transcript ingestor reaches.
func (c *ACPCertifier) applyCertifiedChunk(
	ctx context.Context,
	sourceCtx domain.UsageSourceContext,
	events []domain.ModelUsageEvent,
	lastID int64,
	now time.Time,
) (retired bool, err error) {
	apply := func() error {
		return c.store.ApplyUsageChunk(ctx, sourceCtx.Source.ID, sourceCtx.Source.ByteOffset, sourceCtx.Source.UpdatedAt,
			domain.SourceCursorState{
				ByteOffset:   lastID,
				State:        sourceCtx.Source.State,
				FailureCount: sourceCtx.Source.FailureCount,
				AnomalyCount: sourceCtx.Source.AnomalyCount,
				UpdatedAt:    now,
			}, events, nil)
	}
	if c.pricing != nil {
		apply = func() error {
			return c.pricing.WithSnapshot(ctx, func(snapshot *pricing.Snapshot) error {
				for index := range events {
					c.attributeAndPrice(snapshot, &events[index])
				}
				return c.store.ApplyUsageChunk(ctx, sourceCtx.Source.ID, sourceCtx.Source.ByteOffset, sourceCtx.Source.UpdatedAt,
					domain.SourceCursorState{
						ByteOffset:   lastID,
						State:        sourceCtx.Source.State,
						FailureCount: sourceCtx.Source.FailureCount,
						AnomalyCount: sourceCtx.Source.AnomalyCount,
						UpdatedAt:    now,
					}, events, nil)
			})
		}
	}
	if err := apply(); err != nil {
		if errors.Is(err, domain.ErrUsageSourceEventConflict) {
			if _, markErr := c.store.MarkUsageSourceState(ctx, sourceCtx.Source.ID, domain.UsageSourceComplete,
				domain.UsageErrorSourceEventConflict, nil, now); markErr != nil {
				return false, errors.Join(err, markErr)
			}
			return true, nil
		}
		return false, fmt.Errorf("apply ACP usage chunk for source %d: %w", sourceCtx.Source.ID, err)
	}
	return false, nil
}

// attributeAndPrice attributes the billing provider and prices one event.
// Nothing durable names an ACP event's provider, so the catalog lookup from
// the served model — the same last-resort inference the estimator documents —
// is the only evidence, and it stays replaceable as an inference. A model no
// catalog lists keeps every cost nil: unpriced, never zero.
func (c *ACPCertifier) attributeAndPrice(snapshot *pricing.Snapshot, event *domain.ModelUsageEvent) {
	if event.BillingProviderID == "" {
		if inferred := snapshot.ProviderForModel(event.ModelID); inferred != "" {
			event.BillingProviderID = inferred
			event.BillingProviderSource = domain.UsageBillingProviderInferred
		}
	}
	estimate, estimateErr := snapshot.Estimate(*event)
	if estimateErr != nil {
		event.Costs.PricingVersion = snapshot.ProviderVersion(event.BillingProviderID)
		c.onPricingError(fmt.Errorf("estimate ACP usage event %q: %w", event.SourceEventKey, estimateErr))
		return
	}
	event.Costs = domain.UsageEventCosts{
		InputCostNanos:       estimate.InputNanos,
		CachedInputCostNanos: estimate.CachedInputNanos,
		OutputCostNanos:      estimate.OutputNanos,
		EstimatedCostNanos:   estimate.TotalNanos,
		PricingVersion:       estimate.PricingVersion,
	}
}

func (c *ACPCertifier) settleConversation(
	ctx context.Context,
	terminated bool,
	sourceCtx domain.UsageSourceContext,
	now time.Time,
) error {
	if !terminated || sourceCtx.Source.State == domain.UsageSourceComplete {
		return nil
	}
	if _, err := c.store.MarkUsageSourceState(ctx, sourceCtx.Source.ID, domain.UsageSourceComplete, "", nil, now); err != nil {
		return err
	}
	if sourceCtx.BindingState == domain.UsageBindingComplete ||
		sourceCtx.BindingState == domain.UsageBindingPartial {
		return nil
	}
	if _, err := c.store.UpdateUsageBindingState(ctx, sourceCtx.Source.BindingID, domain.UsageBindingFinalizing, "", now); err != nil {
		return err
	}
	_, err := c.store.CompleteUsageBindingIfSettled(ctx, sourceCtx.Source.BindingID, now)
	return err
}

// acpArchivedUsage mirrors the projected chat usage object the controller
// archives. The driver folds cache reads and writes into CachedTokens and
// drops thought tokens before archiving, so neither is recoverable here.
type acpArchivedUsage struct {
	InputTokens  int64 `json:"InputTokens"`
	OutputTokens int64 `json:"OutputTokens"`
	CachedTokens int64 `json:"CachedTokens"`
	TotalTokens  int64 `json:"TotalTokens"`
	TotalsKnown  bool  `json:"TotalsKnown"`
}

type acpArchivedPayload struct {
	Usage json.RawMessage `json:"usage"`
}

// certifyACPEvents normalizes one archived page into certified events,
// skipping heartbeats, and returns the newest row id it consumed (heartbeat
// rows advance the cursor even though they certify nothing).
func certifyACPEvents(archive []domain.ACPUsageEvent, modelID string) ([]domain.ModelUsageEvent, int64) {
	events := make([]domain.ModelUsageEvent, 0, len(archive))
	lastID := int64(0)
	for _, row := range archive {
		lastID = row.ID
		event, ok := certifyACPEvent(row, modelID)
		if !ok {
			continue
		}
		events = append(events, event)
	}
	return events, lastID
}

func certifyACPEvent(row domain.ACPUsageEvent, modelID string) (domain.ModelUsageEvent, bool) {
	var payload acpArchivedPayload
	if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil || len(payload.Usage) == 0 {
		return domain.ModelUsageEvent{}, false
	}
	var usage acpArchivedUsage
	if err := json.Unmarshal(payload.Usage, &usage); err != nil || !usage.TotalsKnown {
		// A usage object without known totals is a context-occupancy
		// heartbeat: skip it rather than record an invented zero-token
		// request. A known-zero turn keeps TotalsKnown=true and records.
		return domain.ModelUsageEvent{}, false
	}
	event := domain.ModelUsageEvent{
		ProviderID:      domain.UsageProviderACP,
		ModelID:         strings.TrimSpace(modelID),
		MeasurementKind: domain.UsageMeasurementNativeReported,
		CreatedAt:       row.ReceivedAt,
		SourceEventKey:  acpSourceEventKey(row.ID),
	}
	nonOutput := usage.InputTokens + usage.CachedTokens
	event.Tokens = domain.UsageTokenMetrics{
		InputTokens:  &nonOutput,
		OutputTokens: &usage.OutputTokens,
	}
	if isJSONObject(payload.Usage) {
		event.ProviderUsageJSON = string(payload.Usage)
	}
	return event, true
}

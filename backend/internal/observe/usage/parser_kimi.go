package usage

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type kimiWireRecord struct {
	ID      string          `json:"id"`
	Time    json.RawMessage `json:"time"`
	Type    string          `json:"type"`
	Model   string          `json:"model"`
	Usage   json.RawMessage `json:"usage"`
	Message json.RawMessage `json:"message"`
}

type kimiNativeUsage struct {
	InputOther         int64 `json:"inputOther"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
	Output             int64 `json:"output"`
}

func parseKimi(source domain.UsageSourceContext, records []jsonlRecord, state *kimiParserStateV1, result *parseResult) {
	eventsByKey := make(map[string]domain.ModelUsageEvent)
	if state == nil {
		state = &kimiParserStateV1{}
	}
	if state.RoundSeq == 0 {
		state.RoundSeq = 1
	}
	for _, record := range records {
		var native kimiWireRecord
		if err := json.Unmarshal(record.Data, &native); err != nil {
			recordMalformed(result)
			continue
		}
		if native.Type == "message.create" {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(native.Message, &msg) != nil || msg.Role != "user" {
				continue
			}
			anchor := kimiAnchorTime(native.Time)
			switch claudeUserMessageKind(msg.Content) {
			case claudeUserKindPrompt:
				// A user message starts a new round; the first one of a
				// transcript is round 1, a later one after usage records
				// starts the next round.
				if state.HasSteps {
					state.RoundSeq++
					state.HasSteps = false
				}
				state.LastUserTime = anchor
				state.LastUserIsPrompt = true
			case claudeUserKindToolResult:
				state.LastUserTime = anchor
				state.LastUserIsPrompt = false
			}
			continue
		}
		if native.Type != "usage.record" {
			continue
		}
		model := firstNonEmpty(native.Model)
		if model == "" || !jsonValueReported(native.Usage) {
			recordMalformed(result)
			continue
		}
		var usage kimiNativeUsage
		if err := json.Unmarshal(native.Usage, &usage); err != nil {
			recordMalformed(result)
			continue
		}
		tokens, ok := normalizeAnthropicUsage(
			usage.InputOther,
			usage.InputCacheCreation,
			usage.InputCacheRead,
			usage.Output,
			nil,
			nil,
		)
		if !ok {
			recordMalformed(result)
			continue
		}
		recordTime := decodeKimiRecordTime(native.Time)
		identity := firstNonEmpty(native.ID, recordTime.Identity, strconv.FormatInt(record.Offset, 10))
		event := domain.ModelUsageEvent{
			ProviderID:        domain.UsageProviderAnthropic,
			ModelID:           model,
			MeasurementKind:   domain.UsageMeasurementNativeReported,
			Tokens:            tokens,
			ProviderUsageJSON: boundedProviderUsage(native.Usage),
			CreatedAt:         recordTime.Time,
			SourceEventKey: stableSourceEventKey(
				"kimi",
				source.NativeRootID,
				source.Source.SubagentID,
				identity,
				model,
			),
		}
		if existing, duplicate := eventsByKey[event.SourceEventKey]; duplicate {
			if !usageEventsEqual(existing, event) {
				result.Cursor.AnomalyCount++
				result.Cursor.LastErrorCode = domain.UsageErrorSourceEventConflict
			}
			continue
		}
		// Timing (timing ADR Decision 1): LLM elapsed is the usage record minus
		// the most recent user message; first-token is that same interval when
		// the user message started a round. Tool elapsed is not certified for
		// the Kimi wire in V1.
		llmMS := usageIntervalMS(parseUsageTimestamp(state.LastUserTime), recordTime.Time)
		var firstTokenMS *int64
		if state.LastUserIsPrompt {
			firstTokenMS = llmMS
		}
		eventsByKey[event.SourceEventKey] = event
		state.HasSteps = true
		result.Events = append(result.Events, event)
		result.Timing = append(result.Timing, domain.UsageEventTiming{
			SourceEventKey: event.SourceEventKey,
			RoundSeq:       state.RoundSeq,
			LLMMS:          llmMS,
			FirstTokenMS:   firstTokenMS,
		})
	}
}

// kimiAnchorTime returns a parseable RFC3339Nano timestamp for a Kimi wire
// record's time field, or an empty string when it cannot be decoded; an empty
// anchor keeps the derived durations nil.
func kimiAnchorTime(raw json.RawMessage) string {
	decoded := decodeKimiRecordTime(raw)
	if decoded.Time.IsZero() {
		return ""
	}
	return decoded.Time.UTC().Format(time.RFC3339Nano)
}

type kimiRecordTime struct {
	Identity string
	Time     time.Time
}

func decodeKimiRecordTime(raw json.RawMessage) kimiRecordTime {
	if len(raw) == 0 || string(raw) == "null" {
		return kimiRecordTime{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return kimiRecordTime{Identity: text, Time: parseUsageTimestamp(text)}
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return kimiRecordTime{}
	}
	decoded := kimiRecordTime{Identity: number.String()}
	milliseconds, err := number.Int64()
	if err != nil {
		return decoded
	}
	decoded.Time = time.UnixMilli(milliseconds).UTC()
	return decoded
}

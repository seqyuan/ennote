package store

import (
	"context"
	"encoding/json"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
)

// FlowEventSink publishes the Phase 1 flow event set through the EventWriter
// (commit-before-publish): the event is durable in run_events before it is
// published live to SSE consumers.
type FlowEventSink struct {
	Writer *events.Writer
}

// PublishFlow appends one flow event keyed to the flow run id and publishes
// it only after the durable commit succeeds.
func (s *FlowEventSink) PublishFlow(ctx context.Context, runID string, eventType string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if s.Writer == nil {
		return nil // no writer wired (tests without events)
	}
	_, err = s.Writer.Append(ctx, runID, domain.PendingEvent{EventType: eventType, Payload: encoded})
	return err
}

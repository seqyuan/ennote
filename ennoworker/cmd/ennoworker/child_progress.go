package main

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
)

// childProgressPublisher wraps the Hub for a delegated child Run: it forwards
// every live event to the child's own channel (normal behavior) and, in
// addition, translates tool/turn activity boundaries into a live-only
// child_progress event published on the PARENT run's channel. The parent
// surface (nested activity panel) therefore renders per-task activity without
// a second SSE subscription. child_progress is never persisted; delegation
// state transitions remain the durable source of truth.
//
// Event volume is bounded: one child_progress per tool call (first delta) and
// per turn phase change (thinking/writing), and never per streaming fragment.
type childProgressPublisher struct {
	hub         *events.Hub
	childRunID  string
	parentRunID string
	groupID     string
	taskName    string

	mu       sync.Mutex
	reported map[string]struct{} // tool call ids whose activity was already reported
}

func (p *childProgressPublisher) PublishLive(event domain.LiveRunEvent) {
	p.hub.PublishLive(event)
	activity := p.translate(event)
	if activity == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"delegationGroupId": p.groupID,
		"taskName":          p.taskName,
		"childRunId":        p.childRunID,
		"activity":          activity,
		"tokens":            0, // token telemetry stays on durable usage events
	})
	if err != nil {
		return
	}
	p.hub.PublishLive(domain.LiveRunEvent{
		RunID:     p.parentRunID,
		Type:      domain.LiveChildProgress,
		StreamID:  p.taskName, // UI key
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}

// translate maps a child live delta to a bounded activity label, or "" when no
// new activity boundary is crossed. Tool activity is deduplicated per tool
// call id so a streaming call produces exactly one child_progress.
func (p *childProgressPublisher) translate(event domain.LiveRunEvent) string {
	switch event.Type {
	case domain.LiveToolCallDelta:
		var payload struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || payload.ID == "" {
			return ""
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if _, seen := p.reported[payload.ID]; seen {
			return ""
		}
		if p.reported == nil {
			p.reported = make(map[string]struct{})
		}
		p.reported[payload.ID] = struct{}{}
		name := payload.Name
		if name == "" {
			name = "tool"
		}
		return "Running " + name
	case domain.LiveThinkingDelta:
		return "Thinking"
	case domain.LiveTextDelta:
		return "Writing"
	default:
		return ""
	}
}

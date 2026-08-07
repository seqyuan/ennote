package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func publishToolDelta(t *testing.T, publisher *childProgressPublisher, callID, name string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"id": callID, "name": name, "argumentsFragment": "{}"})
	require.NoError(t, err)
	publisher.PublishLive(domain.LiveRunEvent{
		RunID: "child-1", Type: domain.LiveToolCallDelta, StreamID: callID,
		Payload: payload, CreatedAt: time.Now(),
	})
}

// The child's own live channel keeps normal forwarding, and the parent channel
// receives one bounded child_progress per activity boundary.
func TestChildProgressPublisherTranslatesActivity(t *testing.T) {
	hub := events.NewHub()
	parentCh, parentStop := hub.SubscribeLive("parent-1", 64)
	defer parentStop()
	childCh, childStop := hub.SubscribeLive("child-1", 64)
	defer childStop()

	publisher := &childProgressPublisher{
		hub: hub, childRunID: "child-1", parentRunID: "parent-1",
		groupID: "grp-1", taskName: "review", reported: make(map[string]struct{}),
	}

	// Tool call: first delta reports once, later fragments are deduped.
	publishToolDelta(t, publisher, "call-1", "bash")
	publishToolDelta(t, publisher, "call-1", "bash") // same call: no second event
	publishToolDelta(t, publisher, "call-2", "grep") // new call: second event

	thinking, err := json.Marshal(map[string]any{"text": "…"})
	require.NoError(t, err)
	publisher.PublishLive(domain.LiveRunEvent{
		RunID: "child-1", Type: domain.LiveThinkingDelta, StreamID: "t",
		Payload: thinking, CreatedAt: time.Now(),
	})

	// Normal forwarding still reaches the child channel (tool deltas).
	select {
	case ev := <-childCh:
		assert.Equal(t, domain.LiveToolCallDelta, ev.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("child channel did not receive forwarded event")
	}

	// Parent channel: exactly three child_progress events (bash, grep, thinking).
	var activities []string
	for i := 0; i < 3; i++ {
		select {
		case ev := <-parentCh:
			assert.Equal(t, domain.LiveChildProgress, ev.Type)
			assert.Equal(t, "review", ev.StreamID)
			var payload struct {
				DelegationGroupID string `json:"delegationGroupId"`
				TaskName          string `json:"taskName"`
				ChildRunID        string `json:"childRunId"`
				Activity          string `json:"activity"`
			}
			require.NoError(t, json.Unmarshal(ev.Payload, &payload))
			assert.Equal(t, "grp-1", payload.DelegationGroupID)
			assert.Equal(t, "review", payload.TaskName)
			assert.Equal(t, "child-1", payload.ChildRunID)
			activities = append(activities, payload.Activity)
		case <-time.After(2 * time.Second):
			t.Fatalf("parent channel missed child_progress #%d", i)
		}
	}
	assert.Equal(t, []string{"Running bash", "Running grep", "Thinking"}, activities)

	// No further events: dedupe held and no ticker exists.
	select {
	case ev := <-parentCh:
		t.Fatalf("unexpected extra child_progress: %s", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

// Non-activity live events produce no child_progress.
func TestChildProgressPublisherIgnoresOtherEvents(t *testing.T) {
	hub := events.NewHub()
	parentCh, parentStop := hub.SubscribeLive("parent-1", 64)
	defer parentStop()
	publisher := &childProgressPublisher{
		hub: hub, childRunID: "child-1", parentRunID: "parent-1",
		groupID: "grp-1", taskName: "review", reported: make(map[string]struct{}),
	}
	vision, _ := json.Marshal(map[string]any{"description": "x"})
	publisher.PublishLive(domain.LiveRunEvent{
		RunID: "child-1", Type: domain.LiveVisionDescriptionDelta,
		Payload: vision, CreatedAt: time.Now(),
	})
	select {
	case ev := <-parentCh:
		t.Fatalf("unexpected child_progress for %s", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

var _ = context.Background

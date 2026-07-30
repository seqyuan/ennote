package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAppender struct {
	events []domain.RunEvent
	err    error
}

func (f *fakeAppender) Append(_ context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, item := range pending {
		f.events = append(f.events, domain.RunEvent{
			EventID: int64(len(f.events) + 1), RunID: runID, Seq: int64(len(f.events) + 1),
			EventType: item.EventType, Payload: item.Payload, CreatedAt: time.Now(),
		})
	}
	return append([]domain.RunEvent(nil), f.events...), nil
}

func TestWriterPublishesOnlyAfterSuccessfulAppend(t *testing.T) {
	hub := NewHub()
	appender := &fakeAppender{}
	writer := NewWriter(appender, hub)
	channel, unsubscribe := hub.Subscribe("run-1", 2)
	defer unsubscribe()

	events, err := writer.Append(context.Background(), "run-1", domain.PendingEvent{
		EventType: "text_delta", Payload: json.RawMessage(`{"text":"ok"}`),
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	select {
	case published := <-channel:
		assert.Equal(t, events[0].EventID, published.EventID)
	case <-time.After(time.Second):
		t.Fatal("committed event was not published")
	}
}

func TestWriterDoesNotPublishFailedAppend(t *testing.T) {
	hub := NewHub()
	writer := NewWriter(&fakeAppender{err: errors.New("commit failed")}, hub)
	channel, unsubscribe := hub.Subscribe("run-1", 2)
	defer unsubscribe()
	_, err := writer.Append(context.Background(), "run-1", domain.PendingEvent{EventType: "text_delta"})
	require.Error(t, err)
	select {
	case <-channel:
		t.Fatal("failed event must not be published")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	hub := NewHub()
	channel, _ := hub.Subscribe("run-1", 1)
	hub.Publish(
		domain.RunEvent{RunID: "run-1", EventID: 1},
		domain.RunEvent{RunID: "run-1", EventID: 2},
	)
	first, ok := <-channel
	assert.True(t, ok)
	assert.Equal(t, int64(1), first.EventID)
	_, ok = <-channel
	assert.False(t, ok)
}

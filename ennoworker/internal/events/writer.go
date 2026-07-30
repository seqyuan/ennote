package events

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type Appender interface {
	Append(context.Context, string, ...domain.PendingEvent) ([]domain.RunEvent, error)
}

type Writer struct {
	appender Appender
	hub      *Hub
}

func NewWriter(appender Appender, hub *Hub) *Writer {
	if hub == nil {
		hub = NewHub()
	}
	return &Writer{appender: appender, hub: hub}
}

func (w *Writer) Append(ctx context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	events, err := w.appender.Append(ctx, runID, pending...)
	if err != nil {
		return nil, err
	}
	w.hub.Publish(events...)
	return events, nil
}

func (w *Writer) Hub() *Hub { return w.hub }

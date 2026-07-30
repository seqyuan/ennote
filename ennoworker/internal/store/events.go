package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type EventRepo struct{ DB *sql.DB }

func (r *EventRepo) Append(ctx context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	if len(pending) == 0 {
		return nil, nil
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin event transaction: %w", err)
	}
	defer tx.Rollback()

	events, err := appendEventsTx(ctx, tx, runID, pending...)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit events: %w", err)
	}
	return events, nil
}

func (r *EventRepo) After(ctx context.Context, runID string, afterEventID int64, limit int) ([]domain.RunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.DB.QueryContext(ctx,
		`SELECT event_id, run_id, seq, event_type, payload_json, created_at
		 FROM run_events WHERE run_id = ? AND event_id > ? ORDER BY event_id LIMIT ?`,
		runID, afterEventID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query run events: %w", err)
	}
	defer rows.Close()

	var events []domain.RunEvent
	for rows.Next() {
		event, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func appendEventsTx(ctx context.Context, tx *sql.Tx, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	var nextSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, runID,
	).Scan(&nextSeq); err != nil {
		return nil, fmt.Errorf("read next event sequence: %w", err)
	}

	events := make([]domain.RunEvent, 0, len(pending))
	for index, item := range pending {
		payload := item.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if !json.Valid(payload) {
			return nil, fmt.Errorf("event payload for %s is not valid JSON", item.EventType)
		}
		timestamp := time.Now().UTC()
		seq := nextSeq + int64(index)
		result, err := tx.ExecContext(ctx,
			`INSERT INTO run_events (run_id, seq, event_type, payload_json, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			runID, seq, item.EventType, string(payload), timestamp.Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, fmt.Errorf("append run event: %w", err)
		}
		eventID, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read event id: %w", err)
		}
		events = append(events, domain.RunEvent{
			EventID: eventID, RunID: runID, Seq: seq, EventType: item.EventType,
			Payload: append(json.RawMessage(nil), payload...), CreatedAt: timestamp,
		})
	}
	return events, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRunEvent(row rowScanner) (domain.RunEvent, error) {
	var event domain.RunEvent
	var payload, createdAt string
	if err := row.Scan(&event.EventID, &event.RunID, &event.Seq, &event.EventType, &payload, &createdAt); err != nil {
		return event, fmt.Errorf("scan run event: %w", err)
	}
	event.Payload = json.RawMessage(payload)
	var err error
	event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return event, fmt.Errorf("parse run event timestamp: %w", err)
	}
	return event, nil
}

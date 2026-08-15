package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
)

type Projector struct {
	Stores   *Stores
	Sessions *sessionstore.Manager
}

type outboxEvent struct {
	ID        string
	Type      string
	Payload   []byte
	CreatedAt string
}

type sessionUpsertPayload struct {
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type ownerUpsertPayload struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId"`
	ProjectID  string `json:"projectId"`
	SessionID  string `json:"sessionId"`
	UpdatedAt  string `json:"updatedAt"`
}

func (p *Projector) UpsertProject(ctx context.Context, project *domain.Project, workspace *domain.ProjectWorkspace) error {
	if p == nil || p.Stores == nil || project == nil || workspace == nil {
		return fmt.Errorf("project projection is incomplete")
	}
	_, err := p.Stores.Catalog.ExecContext(ctx, `INSERT INTO project_summaries
		(project_id,name,description,status,workspace_path,created_at,updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET name=excluded.name,description=excluded.description,
		status=excluded.status,workspace_path=excluded.workspace_path,updated_at=excluded.updated_at`,
		project.ID, project.Name, project.Description, project.Status, workspace.HostPath,
		project.CreatedAt.Format(time.RFC3339Nano), project.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

type usageAddPayload struct {
	Day           string `json:"day"`
	ProjectID     string `json:"projectId"`
	SessionID     string `json:"sessionId"`
	ProviderID    string `json:"providerId"`
	ModelID       string `json:"modelId"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	CacheRead     int64  `json:"cacheReadTokens"`
	Reasoning     int64  `json:"reasoningTokens"`
	CostUSDMicros int64  `json:"costUsdMicros"`
	UpdatedAt     string `json:"updatedAt"`
}

func (p *Projector) DrainSession(ctx context.Context, sessionID string, limit int) (int, error) {
	if p == nil || p.Stores == nil || p.Sessions == nil {
		return 0, fmt.Errorf("projector is incomplete")
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	sessionDB, err := p.Sessions.OpenSession(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	rows, err := sessionDB.QueryContext(ctx, `SELECT event_id,event_type,payload_json,created_at
		FROM projection_outbox WHERE projected_at IS NULL ORDER BY created_at,event_id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	events := make([]outboxEvent, 0)
	for rows.Next() {
		var event outboxEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.Payload, &event.CreatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	processed := 0
	for _, event := range events {
		if err := p.applyCatalog(ctx, sessionID, event); err != nil {
			return processed, err
		}
		if err := p.applyUsage(ctx, sessionID, event); err != nil {
			return processed, err
		}
		result, err := sessionDB.ExecContext(ctx, `UPDATE projection_outbox SET projected_at=?
			WHERE event_id=? AND projected_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), event.ID)
		if err != nil {
			return processed, err
		}
		if changed, _ := result.RowsAffected(); changed == 1 {
			processed++
		}
	}
	return processed, nil
}

func (p *Projector) applyCatalog(ctx context.Context, sessionID string, event outboxEvent) error {
	tx, err := p.Stores.Catalog.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := recordProjectionEvent(ctx, tx, event, sessionID)
	if err != nil || !inserted {
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	switch event.Type {
	case "session.upsert":
		var payload sessionUpsertPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO session_summaries
			(session_id,project_id,title,status,created_at,updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(session_id) DO UPDATE SET project_id=excluded.project_id,title=excluded.title,
			status=excluded.status,updated_at=excluded.updated_at`, payload.SessionID, payload.ProjectID,
			payload.Title, payload.Status, payload.CreatedAt, payload.UpdatedAt)
	case "session.delete":
		var payload struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM session_summaries WHERE session_id=?`, payload.SessionID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM owner_index WHERE session_id=?`, payload.SessionID)
		}
	case "owner.upsert":
		var payload ownerUpsertPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO owner_index
			(resource_kind,resource_id,project_id,session_id,updated_at) VALUES(?,?,?,?,?)
			ON CONFLICT(resource_kind,resource_id) DO UPDATE SET project_id=excluded.project_id,
			session_id=excluded.session_id,updated_at=excluded.updated_at`, payload.Kind,
			payload.ResourceID, payload.ProjectID, payload.SessionID, payload.UpdatedAt)
	case "usage.add":
		// Usage is intentionally absent from catalog.db.
	default:
		return fmt.Errorf("unsupported projection event type %q", event.Type)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Projector) applyUsage(ctx context.Context, sessionID string, event outboxEvent) error {
	tx, err := p.Stores.Usage.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := recordProjectionEvent(ctx, tx, event, sessionID)
	if err != nil || !inserted {
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if event.Type == "usage.add" {
		var payload usageAddPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_aggregates
			(usage_day,project_id,session_id,provider_id,model_id,input_tokens,output_tokens,
			 cache_read_tokens,reasoning_tokens,cost_usd_micros,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(usage_day,project_id,session_id,provider_id,model_id) DO UPDATE SET
			input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
			cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,
			reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens,
			cost_usd_micros=cost_usd_micros+excluded.cost_usd_micros,updated_at=excluded.updated_at`,
			payload.Day, payload.ProjectID, payload.SessionID, payload.ProviderID, payload.ModelID,
			payload.InputTokens, payload.OutputTokens, payload.CacheRead, payload.Reasoning,
			payload.CostUSDMicros, payload.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordProjectionEvent(ctx context.Context, tx *sql.Tx, event outboxEvent, sessionID string) (bool, error) {
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO applied_projection_events
		(event_id,session_id,event_type,applied_at) VALUES(?,?,?,?)`, event.ID, sessionID,
		event.Type, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

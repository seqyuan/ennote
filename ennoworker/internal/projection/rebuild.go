package projection

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
)

type Rebuilder struct {
	Stores   *Stores
	Projects *projectstore.Store
	Sessions *sessionstore.Manager
}

func (r *Rebuilder) Rebuild(ctx context.Context) error {
	if r == nil || r.Stores == nil || r.Projects == nil || r.Sessions == nil {
		return fmt.Errorf("projection rebuilder is incomplete")
	}
	catalogTx, err := r.Stores.Catalog.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer catalogTx.Rollback()
	for _, table := range []string{"project_summaries", "session_summaries", "owner_index", "attention_summaries", "resource_diagnostics", "applied_projection_events"} {
		if _, err := catalogTx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	usageTx, err := r.Stores.Usage.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer usageTx.Rollback()
	if _, err := usageTx.ExecContext(ctx, `DELETE FROM usage_aggregates`); err != nil {
		return err
	}
	if _, err := usageTx.ExecContext(ctx, `DELETE FROM applied_projection_events`); err != nil {
		return err
	}

	projects, err := r.Projects.List(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		workspace, err := r.Projects.FindWorkspaceByProjectID(ctx, project.ID)
		if err != nil {
			return err
		}
		if workspace == nil {
			return fmt.Errorf("project %s has no workspace manifest", project.ID)
		}
		if _, err := catalogTx.ExecContext(ctx, `INSERT INTO project_summaries
			(project_id,name,description,status,workspace_path,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			project.ID, project.Name, project.Description, project.Status, workspace.HostPath,
			project.CreatedAt.Format(time.RFC3339Nano), project.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		for _, status := range []string{"active", "archived"} {
			sessions, err := r.Sessions.ListByProject(ctx, project.ID, status)
			if err != nil {
				return err
			}
			for _, session := range sessions {
				if _, err := catalogTx.ExecContext(ctx, `INSERT INTO session_summaries
					(session_id,project_id,title,status,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
					session.ID, project.ID, session.Title, session.Status,
					session.CreatedAt.Format(time.RFC3339Nano), session.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
					return err
				}
				db, err := r.Sessions.OpenSession(ctx, session.ID)
				if err != nil {
					return err
				}
				if err := rebuildOwners(ctx, catalogTx, db, project.ID, session.ID); err != nil {
					return err
				}
				if err := rebuildAttention(ctx, catalogTx, db, project.ID, session.ID); err != nil {
					return err
				}
				if err := rebuildUsage(ctx, usageTx, db, project.ID, session.ID); err != nil {
					return err
				}
			}
		}
	}
	if err := catalogTx.Commit(); err != nil {
		return err
	}
	return usageTx.Commit()
}

var ownerTables = []struct {
	kind   string
	table  string
	column string
}{
	{"run", "agent_runs", "id"},
	{"message", "messages", "id"},
	{"compaction", "context_compactions", "id"},
	{"approval", "tool_approval_requests", "id"},
	{"standing_approval", "standing_approvals", "id"},
	{"delegation_group", "delegation_groups", "id"},
	{"delegation_item", "delegation_items", "id"},
	{"delegation_handle", "delegation_handles", "id"},
	{"delegation_approval", "delegation_approval_requests", "id"},
	{"attention", "attention_items", "id"},
	{"artifact", "artifacts", "id"},
	{"flow_run", "run_agent_flow", "run_id"},
	{"mcp_request", "mcp_requests", "id"},
}

func rebuildOwners(ctx context.Context, tx *sql.Tx, sessionDB *sql.DB, projectID, sessionID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, source := range ownerTables {
		rows, err := sessionDB.QueryContext(ctx, "SELECT "+source.column+" FROM "+source.table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO owner_index
				(resource_kind,resource_id,project_id,session_id,updated_at) VALUES(?,?,?,?,?)`,
				source.kind, id, projectID, sessionID, now); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func rebuildAttention(ctx context.Context, tx *sql.Tx, sessionDB *sql.DB, projectID, sessionID string) error {
	rows, err := sessionDB.QueryContext(ctx, `SELECT id,kind,status,requires_action,display_json,created_at,
		COALESCE(resolved_at,dismissed_at,created_at) FROM attention_items`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, status, display, createdAt, updatedAt string
		var requiresAction int
		if err := rows.Scan(&id, &kind, &status, &requiresAction, &display, &createdAt, &updatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attention_summaries
			(attention_id,project_id,session_id,kind,status,requires_action,display_json,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, id, projectID, sessionID, kind, status, requiresAction, display, createdAt, updatedAt); err != nil {
			return err
		}
	}
	return rows.Err()
}

func rebuildUsage(ctx context.Context, tx *sql.Tx, sessionDB *sql.DB, projectID, sessionID string) error {
	rows, err := sessionDB.QueryContext(ctx, `SELECT substr(mc.started_at,1,10),COALESCE(mc.provider_profile_id,''),
		COALESCE(mc.model_profile_id,mc.actual_model,''),SUM(mc.input_tokens),SUM(mc.output_tokens),
		SUM(mc.cache_read_tokens),SUM(mc.reasoning_tokens),MAX(mc.started_at)
		FROM model_calls mc GROUP BY 1,2,3`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day, providerID, modelID, updatedAt string
		var input, output, cacheRead, reasoning int64
		if err := rows.Scan(&day, &providerID, &modelID, &input, &output, &cacheRead, &reasoning, &updatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_aggregates
			(usage_day,project_id,session_id,provider_id,model_id,input_tokens,output_tokens,
			 cache_read_tokens,reasoning_tokens,cost_usd_micros,updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,0,?)`, day, projectID, sessionID, providerID, modelID,
			input, output, cacheRead, reasoning, updatedAt); err != nil {
			return err
		}
	}
	return rows.Err()
}

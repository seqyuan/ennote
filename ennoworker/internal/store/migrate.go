package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/migrations"
)

// Migrate applies all outstanding schema migrations.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	pending := make([]migrations.Migration, 0)
	rebuildsTables := false
	for _, migration := range migrations.Sorted() {
		if migration.Version <= current {
			continue
		}
		pending = append(pending, migration)
		if migration.Version == 7 || migration.Version == 21 || migration.Version == 23 || migration.Version == 26 {
			rebuildsTables = true
		}
	}
	if len(pending) == 0 {
		return nil
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()
	if rebuildsTables {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("disable foreign keys for table rebuild: %w", err)
		}
		defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, migration := range pending {
		if _, err := tx.Exec(migration.SQL); err != nil {
			return fmt.Errorf("migration %d: %w", migration.Version, err)
		}
		if migration.Version == 18 {
			if err := backfillHostedPromptSnapshots(context.Background(), tx); err != nil {
				return fmt.Errorf("migration 18 prompt snapshots: %w", err)
			}
		}
		if migration.Version == 24 {
			if err := BackfillDelegationGenerations(context.Background(), tx); err != nil {
				return fmt.Errorf("migration 24 delegation generation backfill: %w", err)
			}
		}
		if migration.Version == 25 {
			if err := BackfillDelegationHandles(context.Background(), tx); err != nil {
				return fmt.Errorf("migration 25 delegation handle backfill: %w", err)
			}
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			migration.Version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
	}
	if rebuildsTables {
		rows, err := tx.Query("PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("check rebuilt foreign keys: %w", err)
		}
		if rows.Next() {
			var table string
			var rowID int64
			var parent string
			var foreignKey int
			_ = rows.Scan(&table, &rowID, &parent, &foreignKey)
			rows.Close()
			return fmt.Errorf("foreign key check failed: table=%s row=%d parent=%s fk=%d", table, rowID, parent, foreignKey)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rebuildsTables {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("re-enable foreign keys: %w", err)
		}
	}
	return nil
}

func backfillHostedPromptSnapshots(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT ar.id,s.default_agent_profile_id,COALESCE(ap.system_prompt,'')
		FROM agent_runs ar JOIN sessions s ON s.id=ar.session_id
		LEFT JOIN agent_profiles ap ON ap.id=s.default_agent_profile_id`)
	if err != nil {
		return err
	}
	type entry struct{ runID, agentID, prompt string }
	entries := make([]entry, 0)
	for rows.Next() {
		var item entry
		var agentID sql.NullString
		if err := rows.Scan(&item.runID, &agentID, &item.prompt); err != nil {
			rows.Close()
			return err
		}
		if agentID.Valid {
			item.agentID = agentID.String
		}
		entries = append(entries, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range entries {
		snapshot, err := newSystemPromptSnapshot(item.agentID, item.prompt)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET system_prompt_snapshot_json=?,system_prompt_digest=? WHERE id=?`,
			string(encoded), snapshot.Digest, item.runID); err != nil {
			return err
		}
	}
	return nil
}

// backfillDelegationGenerations creates generation 0 and one attempt per child
// Run for every delegation group that existed before migration 24. IDs are
// deterministic so a retried migration startup is idempotent; snapshots and
// digests reuse the exact runtime helpers.
func BackfillDelegationGenerations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.parent_run_id,g.status,g.created_at,
		i.id,i.child_run_id,i.name,i.role_version_id,i.output_contract,i.budget_json,i.result_json,i.status,i.ordinal
		FROM delegation_groups g JOIN delegation_items i ON i.group_id=g.id
		ORDER BY g.id,i.ordinal`)
	if err != nil {
		return err
	}
	type backfillItem struct {
		itemID, childRunID, name, roleVersionID, outputContract, budgetJSON, resultJSON, itemStatus string
		ordinal                                                                                     int
	}
	type groupEntry struct {
		groupID, parentRunID, groupStatus, groupCreatedAt string
		items                                             []backfillItem
	}
	groups := make([]groupEntry, 0)
	groupIndex := make(map[string]int)
	for rows.Next() {
		var entry backfillItem
		var groupID, parentRunID, groupStatus, groupCreatedAt string
		var childRun sql.NullString
		var result sql.NullString
		if err := rows.Scan(&groupID, &parentRunID, &groupStatus, &groupCreatedAt,
			&entry.itemID, &childRun, &entry.name, &entry.roleVersionID, &entry.outputContract,
			&entry.budgetJSON, &result, &entry.itemStatus, &entry.ordinal); err != nil {
			rows.Close()
			return err
		}
		entry.childRunID = childRun.String
		entry.resultJSON = result.String
		index, ok := groupIndex[groupID]
		if !ok {
			index = len(groups)
			groupIndex[groupID] = index
			groups = append(groups, groupEntry{groupID: groupID, parentRunID: parentRunID,
				groupStatus: groupStatus, groupCreatedAt: groupCreatedAt})
		}
		groups[index].items = append(groups[index].items, entry)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, group := range groups {
		generationID := "backfill-gen-" + group.groupID
		authSnapshot := make([]map[string]string, 0, len(group.items))
		var budget domain.BudgetCeilingJSON
		for _, item := range group.items {
			authSnapshot = append(authSnapshot, map[string]string{
				"itemId": item.itemID, "roleVersionId": item.roleVersionID,
			})
			var ceiling domain.BudgetCeilingJSON
			if item.budgetJSON != "" {
				if err := json.Unmarshal([]byte(item.budgetJSON), &ceiling); err != nil {
					return fmt.Errorf("decode backfill budget for %s: %w", item.itemID, err)
				}
			}
			budget.MaxModelCalls += ceiling.MaxModelCalls
			budget.MaxToolCalls += ceiling.MaxToolCalls
			budget.MaxTotalTokens += ceiling.MaxTotalTokens
			budget.MaxOutputTokens += ceiling.MaxOutputTokens
			budget.MaxCostMicros += ceiling.MaxCostMicros
		}
		authJSON, err := json.Marshal(authSnapshot)
		if err != nil {
			return err
		}
		authDigest, err := digestJSON(authSnapshot)
		if err != nil {
			return err
		}
		budgetJSON, err := json.Marshal(budget)
		if err != nil {
			return err
		}
		budgetDigest, err := digestJSON(budget)
		if err != nil {
			return err
		}
		generationStatus := map[string]string{
			"settled": "settled", "cancelled": "cancelled", "waiting_children": "running",
			"pending": "queued", "waiting_admission": "queued",
		}[group.groupStatus]
		if generationStatus == "" {
			generationStatus = "queued"
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_group_generations
			(id,group_id,generation,kind,status,retry_selection_json,reused_attempts_json,
			 authorization_snapshot_json,authorization_snapshot_digest,budget_snapshot_json,budget_snapshot_digest,
			 client_request_id,created_at,completed_at)
			VALUES(?,?,0,'initial',?,'[]','[]',?,?,?,?,?,?,?)`,
			generationID, group.groupID, generationStatus, string(authJSON), authDigest,
			string(budgetJSON), budgetDigest, "backfill:"+group.groupID, group.groupCreatedAt,
			nullableBackfillTime(group.groupStatus)); err != nil {
			return fmt.Errorf("backfill generation %s: %w", generationID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET current_generation=0,updated_at=?
			WHERE id=? AND current_generation=0`, group.groupCreatedAt, group.groupID); err != nil {
			return err
		}

		for _, item := range group.items {
			if item.childRunID == "" {
				continue
			}
			attemptID := "backfill-att-" + item.itemID
			roleSnapshot := map[string]string{
				"itemId": item.itemID, "roleVersionId": item.roleVersionID,
				"outputContract": item.outputContract,
			}
			roleJSON, err := json.Marshal(roleSnapshot)
			if err != nil {
				return err
			}
			roleDigest, err := digestJSON(roleSnapshot)
			if err != nil {
				return err
			}
			attemptStatus := backfillAttemptStatus(item.itemStatus, item.childRunID, tx)
			var resultDigest string
			if item.resultJSON != "" {
				resultDigest, err = digestJSON(json.RawMessage(item.resultJSON))
				if err != nil {
					return err
				}
			}
			var reconciled sql.NullString
			_ = tx.QueryRowContext(ctx, `SELECT root_reconciled_at FROM run_budgets WHERE run_id=?`,
				item.childRunID).Scan(&reconciled)
			usage := readChildUsageTx(ctx, tx, item.childRunID)
			usageJSON, err := json.Marshal(usage)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_item_attempts
				(id,item_id,generation,retry_of_attempt_id,child_run_id,
				 authorization_snapshot_json,authorization_snapshot_digest,reserved_budget_json,actual_usage_json,
				 status,result_json,result_digest,root_reconciled_at,created_at)
				VALUES(?,?,0,NULL,?,?,?,?,?,?,?,?,?,?)`,
				attemptID, item.itemID, item.childRunID, string(roleJSON), roleDigest,
				item.budgetJSON, string(usageJSON), attemptStatus,
				nullableBackfillJSON(item.resultJSON), resultDigest,
				nullableBackfillString(reconciled), group.groupCreatedAt); err != nil {
				return fmt.Errorf("backfill attempt %s: %w", attemptID, err)
			}
		}
	}
	return nil
}

func backfillAttemptStatus(itemStatus, childRunID string, tx *sql.Tx) string {
	if itemStatus == "succeeded" || itemStatus == "failed" || itemStatus == "cancelled" ||
		itemStatus == "not_authorized" {
		return itemStatus
	}
	// A pending/running item whose child Run was interrupted by recovery maps
	// to interrupted; anything else stays queued.
	var childStatus string
	if err := tx.QueryRowContext(context.Background(), `SELECT status FROM agent_runs WHERE id=?`, childRunID).Scan(&childStatus); err == nil {
		if childStatus == "interrupted" {
			return "interrupted"
		}
	}
	return "queued"
}

func readChildUsageTx(ctx context.Context, tx *sql.Tx, childRunID string) map[string]int64 {
	usage := map[string]int64{"modelCalls": 0, "toolCalls": 0, "tokens": 0, "outputTokens": 0, "costMicros": 0}
	var modelCalls, toolCalls int
	var tokens, outputTokens, costMicros int64
	if err := tx.QueryRowContext(ctx, `SELECT consumed_model_calls,consumed_tool_calls,consumed_tokens,
		consumed_output_tokens,consumed_cost_usd_micros FROM run_budgets WHERE run_id=?`, childRunID).
		Scan(&modelCalls, &toolCalls, &tokens, &outputTokens, &costMicros); err != nil {
		return usage
	}
	usage["modelCalls"] = int64(modelCalls)
	usage["toolCalls"] = int64(toolCalls)
	usage["tokens"] = tokens
	usage["outputTokens"] = outputTokens
	usage["costMicros"] = costMicros
	return usage
}

func nullableBackfillTime(status string) any {
	if status == "settled" || status == "cancelled" {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return nil
}

func nullableBackfillJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBackfillString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

// BackfillDelegationHandles creates one stable handle per delegation group and
// one logical completion per terminal generation 0, both in blocking mode and
// marked consumed by the original parent. Deterministic IDs make a retried
// migration startup idempotent.
func BackfillDelegationHandles(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.parent_run_id,g.status,g.created_at,
		s.id,COALESCE(s.active_branch_id,'')
		FROM delegation_groups g
		JOIN agent_runs ar ON ar.id=g.parent_run_id
		JOIN sessions s ON s.id=ar.session_id
		ORDER BY g.created_at,g.id`)
	if err != nil {
		return err
	}
	type groupRow struct {
		groupID, parentRunID, groupStatus, groupCreatedAt, sessionID, branchID string
	}
	groups := make([]groupRow, 0)
	for rows.Next() {
		var entry groupRow
		if err := rows.Scan(&entry.groupID, &entry.parentRunID, &entry.groupStatus,
			&entry.groupCreatedAt, &entry.sessionID, &entry.branchID); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, entry)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, group := range groups {
		if group.branchID == "" {
			continue // no stable branch: skip (defensive)
		}
		handleID := "backfill-handle-" + group.groupID
		handleStatus := "active"
		switch group.groupStatus {
		case "settled":
			handleStatus = "completed"
		case "cancelled":
			handleStatus = "cancelled"
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_handles
			(id,group_id,session_id,source_parent_run_id,source_branch_id,execution_mode,auto_resume,status,created_at,updated_at)
			VALUES(?,?,?,?,?, 'blocking',0,?,?,?)`,
			handleID, group.groupID, group.sessionID, group.parentRunID, group.branchID,
			handleStatus, group.groupCreatedAt, group.groupCreatedAt); err != nil {
			return fmt.Errorf("backfill handle %s: %w", handleID, err)
		}
		if group.groupStatus != "settled" && group.groupStatus != "cancelled" {
			continue
		}
		// Terminal generation 0 gets exactly one consumed-by-parent completion.
		resultJSON, err := foldGenerationZeroResult(ctx, tx, group.groupID, group.groupStatus)
		if err != nil {
			return err
		}
		digest, err := digestJSON(json.RawMessage(resultJSON))
		if err != nil {
			return err
		}
		sequence, err := nextDeliverySequenceTx(ctx, tx, group.sessionID)
		if err != nil {
			return err
		}
		completionID := "backfill-completion-" + group.groupID + "-0"
		kind := "completed"
		if group.groupStatus == "cancelled" {
			kind = "cancelled"
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_completions
			(id,handle_id,session_id,generation,kind,result_json,result_digest,sequence,delivery_status,created_at)
			VALUES(?,?,?,0,?,?,?,?, 'consumed_by_parent',?)`,
			completionID, handleID, group.sessionID, kind, resultJSON, digest, sequence,
			group.groupCreatedAt); err != nil {
			return fmt.Errorf("backfill completion %s: %w", completionID, err)
		}
	}
	return nil
}

// foldGenerationZeroResult aggregates generation-0 items into the completion
// result payload, mirroring the folded tool result shape.
func foldGenerationZeroResult(ctx context.Context, tx *sql.Tx, groupID, groupStatus string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,COALESCE(result_json,''),status FROM delegation_items
		WHERE group_id=? ORDER BY ordinal`, groupID)
	if err != nil {
		return "", err
	}
	type childResult struct {
		Name   string          `json:"name"`
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	children := make([]childResult, 0)
	for rows.Next() {
		var name, status string
		var result sql.NullString
		if err := rows.Scan(&name, &result, &status); err != nil {
			rows.Close()
			return "", err
		}
		res := json.RawMessage("null")
		if result.Valid && result.String != "" {
			res = json.RawMessage(result.String)
		}
		children = append(children, childResult{Name: name, Status: status, Result: res})
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	payload := map[string]any{"status": groupStatus, "children": children}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// nextDeliverySequenceTx allocates the next session-wide delivery sequence.
func nextDeliverySequenceTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT next_sequence FROM session_delivery_sequences
		WHERE session_id=?`, sessionID).Scan(&next); err != nil {
		if err != sql.ErrNoRows {
			return 0, err
		}
		next = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_delivery_sequences (session_id,next_sequence)
		VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET next_sequence=next_sequence+1`,
		sessionID, next+1); err != nil {
		return 0, err
	}
	return next, nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type MessageRepo struct{ DB *sql.DB }

var ErrMessageCursorInvalid = errors.New("message cursor is not on the active session lineage")

func (r *MessageRepo) CreateUserMessage(ctx context.Context, sessionID, parentID, text string) (*domain.Message, error) {
	timestamp := time.Now().UTC()
	messageID := uuid.NewString()
	partID := uuid.NewString()
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, fmt.Errorf("encode message part: %w", err)
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin message transaction: %w", err)
	}
	defer tx.Rollback()

	if parentID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM messages WHERE id = ? AND session_id = ?`,
			parentID, sessionID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("validate parent message: %w", err)
		}
		if exists != 1 {
			return nil, fmt.Errorf("parent message does not belong to session: %s", parentID)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, parent_message_id, role, status, created_at)
		 VALUES (?, ?, ?, 'user', 'complete', ?)`,
		messageID, sessionID, nullableStr(parentID), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("create message: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_parts (id, message_id, ordinal, block_kind, payload_json)
		 VALUES (?, ?, 0, 'text', ?)`,
		partID, messageID, string(payload),
	); err != nil {
		return nil, fmt.Errorf("create message part: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit message: %w", err)
	}

	return &domain.Message{
		ID:              messageID,
		SessionID:       sessionID,
		ParentMessageID: strPtr(parentID),
		Role:            "user",
		Status:          "complete",
		CreatedAt:       timestamp,
	}, nil
}

func (r *MessageRepo) Lineage(ctx context.Context, sessionID, leafID string) ([]domain.Message, error) {
	const query = `WITH RECURSIVE chain(id, session_id, parent_message_id, role, status, run_id, created_at, depth) AS (
		SELECT id, session_id, parent_message_id, role, status, run_id, created_at, 0
		FROM messages WHERE id = ? AND session_id = ?
		UNION ALL
		SELECT m.id, m.session_id, m.parent_message_id, m.role, m.status, m.run_id, m.created_at, c.depth + 1
		FROM messages m JOIN chain c
		  ON m.id = c.parent_message_id AND m.session_id = c.session_id
		WHERE c.depth < 500
	)
	SELECT id, session_id, parent_message_id, role, status, run_id, created_at
	FROM chain ORDER BY depth DESC`

	rows, err := r.DB.QueryContext(ctx, query, leafID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("lineage query: %w", err)
	}
	var messages []domain.Message
	for rows.Next() {
		var message domain.Message
		var createdAt string
		var runID sql.NullString
		if err := rows.Scan(
			&message.ID,
			&message.SessionID,
			&message.ParentMessageID,
			&message.Role,
			&message.Status,
			&runID,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan lineage message: %w", err)
		}
		if runID.Valid {
			message.RunID = &runID.String
		}
		message.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse message timestamp: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read lineage: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close lineage rows: %w", err)
	}
	for index := range messages {
		parts, err := loadMessageParts(ctx, r.DB, messages[index].ID)
		if err != nil {
			return nil, err
		}
		messages[index].Parts = parts
	}
	return messages, nil
}

func (r *MessageRepo) Page(ctx context.Context, sessionID, leafID, beforeMessageID string, limit int) (domain.MessagePage, error) {
	page := domain.MessagePage{Messages: []domain.Message{}}
	if limit <= 0 {
		return page, nil
	}
	if leafID == "" {
		if beforeMessageID != "" {
			return page, ErrMessageCursorInvalid
		}
		return page, nil
	}

	anchorID := leafID
	if beforeMessageID != "" {
		const cursorQuery = `WITH RECURSIVE chain(id,parent_message_id) AS (
			SELECT id,parent_message_id FROM messages WHERE id=? AND session_id=?
			UNION
			SELECT m.id,m.parent_message_id
			FROM messages m JOIN chain c ON m.id=c.parent_message_id AND m.session_id=?
			WHERE c.id<>?
		)
		SELECT parent_message_id FROM chain WHERE id=?`
		var parent sql.NullString
		if err := r.DB.QueryRowContext(ctx, cursorQuery, leafID, sessionID, sessionID, beforeMessageID, beforeMessageID).Scan(&parent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return page, ErrMessageCursorInvalid
			}
			return page, fmt.Errorf("validate message cursor: %w", err)
		}
		if !parent.Valid {
			return page, nil
		}
		anchorID = parent.String
	}

	const pageQuery = `WITH RECURSIVE page_chain(id,session_id,parent_message_id,role,status,run_id,created_at,depth,path) AS (
		SELECT id,session_id,parent_message_id,role,status,run_id,created_at,0,'|' || id || '|'
		FROM messages WHERE id=? AND session_id=?
		UNION ALL
		SELECT m.id,m.session_id,m.parent_message_id,m.role,m.status,m.run_id,m.created_at,c.depth+1,c.path || m.id || '|'
		FROM messages m JOIN page_chain c ON m.id=c.parent_message_id AND m.session_id=c.session_id
		WHERE c.depth < ? AND instr(c.path,'|' || m.id || '|')=0
	)
	SELECT c.id,c.session_id,c.parent_message_id,c.role,c.status,c.run_id,c.created_at,
		p.ordinal,p.block_kind,p.payload_json
	FROM page_chain c LEFT JOIN message_parts p ON p.message_id=c.id
	ORDER BY c.depth ASC,p.ordinal ASC`
	rows, err := r.DB.QueryContext(ctx, pageQuery, anchorID, sessionID, limit)
	if err != nil {
		return page, fmt.Errorf("message page query: %w", err)
	}
	defer rows.Close()

	newestFirst := make([]domain.Message, 0, limit+1)
	for rows.Next() {
		var id, rowSessionID, role, status, createdAt string
		var parentID, runID sql.NullString
		var ordinal sql.NullInt64
		var kind, payload sql.NullString
		if err := rows.Scan(&id, &rowSessionID, &parentID, &role, &status, &runID, &createdAt,
			&ordinal, &kind, &payload); err != nil {
			return page, fmt.Errorf("scan message page: %w", err)
		}
		if len(newestFirst) == 0 || newestFirst[len(newestFirst)-1].ID != id {
			created, parseErr := time.Parse(time.RFC3339Nano, createdAt)
			if parseErr != nil {
				return page, fmt.Errorf("parse message timestamp: %w", parseErr)
			}
			message := domain.Message{ID: id, SessionID: rowSessionID, Role: role, Status: status,
				Parts: []domain.ContentBlock{}, CreatedAt: created}
			if parentID.Valid {
				message.ParentMessageID = &parentID.String
			}
			if runID.Valid {
				message.RunID = &runID.String
			}
			newestFirst = append(newestFirst, message)
		}
		if ordinal.Valid && kind.Valid && payload.Valid {
			block, decodeErr := decodeContentBlock(domain.ContentKind(kind.String), json.RawMessage(payload.String))
			if decodeErr != nil {
				return page, fmt.Errorf("decode %s message part: %w", kind.String, decodeErr)
			}
			last := len(newestFirst) - 1
			newestFirst[last].Parts = append(newestFirst[last].Parts, block)
		}
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("read message page: %w", err)
	}

	if len(newestFirst) > limit {
		page.HasMore = true
		newestFirst = newestFirst[:limit]
	}
	for left, right := 0, len(newestFirst)-1; left < right; left, right = left+1, right-1 {
		newestFirst[left], newestFirst[right] = newestFirst[right], newestFirst[left]
	}
	page.Messages = newestFirst
	if page.HasMore && len(page.Messages) > 0 {
		page.NextBeforeMessageID = page.Messages[0].ID
	}
	return page, nil
}

func nullableStr(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

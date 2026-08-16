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

// nextMessageSeq returns the next session-monotonic message seq (MAX(seq)+1).
// Callers within a multi-message transaction must call it once per inserted
// message so each message receives a unique, increasing seq. The session DB is
// per-session, but the filter keeps parity with the existing defensive style.
func nextMessageSeq(ctx context.Context, tx *sql.Tx, sessionID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?`, sessionID,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("allocate message seq: %w", err)
	}
	return seq, nil
}

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

	seq, err := nextMessageSeq(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, parent_message_id, role, status,
		 speaker_kind, speaker_snapshot_json, addressee_kind, visibility, originated_at, created_at, seq)
		 VALUES (?, ?, ?, 'user', 'complete', 'user', '{"kind":"user","displayName":"You"}',
		 'host', 'public', ?, ?, ?)`,
		messageID, sessionID, nullableStr(parentID), timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano), seq,
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
		SpeakerKind:     domain.SpeakerUser,
		SpeakerSnapshot: json.RawMessage(`{"kind":"user","displayName":"You"}`),
		AddresseeKind:   strPtr("host"),
		Visibility:      domain.VisibilityPublic,
		OriginatedAt:    &timestamp,
		CreatedAt:       timestamp,
		Seq:             seq,
	}, nil
}

func (r *MessageRepo) Lineage(ctx context.Context, sessionID, leafID string) ([]domain.Message, error) {
	var unsupported int
	if err := r.DB.QueryRowContext(ctx, `WITH RECURSIVE chain(id,parent_message_id,run_id,depth) AS (
		SELECT id,parent_message_id,run_id,0 FROM messages WHERE id=? AND session_id=?
		UNION ALL SELECT m.id,m.parent_message_id,m.run_id,c.depth+1 FROM messages m
		JOIN chain c ON m.id=c.parent_message_id WHERE m.session_id=? AND c.depth<500
	) SELECT COUNT(*) FROM chain c JOIN agent_runs ar ON ar.id=c.run_id
	WHERE ar.commit_format_version=2`, leafID, sessionID, sessionID).Scan(&unsupported); err != nil {
		return nil, fmt.Errorf("validate lineage format: %w", err)
	}
	if unsupported != 0 {
		return nil, domain.NewCodedError(domain.ErrorContextProjectionNotEnabled,
			fmt.Errorf("lineage contains %d format 2 run messages", unsupported))
	}
	return r.loadLineage(ctx, sessionID, leafID)
}

// HostedContextLineage reads canonical mixed-format history for target-aware
// execution and compaction. Private execution rows and room protocol facts are
// intentionally not projected into model context.
func (r *MessageRepo) HostedContextLineage(ctx context.Context, sessionID, leafID string) ([]domain.Message, error) {
	lineage, err := r.loadLineage(ctx, sessionID, leafID)
	if err != nil {
		return nil, err
	}
	visible := make([]domain.Message, 0, len(lineage))
	for _, message := range lineage {
		if message.Visibility == domain.VisibilityPublic || message.Visibility == domain.VisibilityLegacyExecution {
			visible = append(visible, message)
		}
	}
	return visible, nil
}

func (r *MessageRepo) loadLineage(ctx context.Context, sessionID, leafID string) ([]domain.Message, error) {
	const query = `WITH RECURSIVE chain(id, session_id, parent_message_id, role, status, run_id,
		speaker_kind, speaker_object_id, speaker_version_id, participant_instance_id, speaker_snapshot_json,
		addressee_kind, addressee_object_id, addressee_version_id, reply_to_message_id, visibility,
		originated_at, created_at, seq, depth) AS (
		SELECT id, session_id, parent_message_id, role, status, run_id,
			speaker_kind, speaker_object_id, speaker_version_id, participant_instance_id, speaker_snapshot_json,
			addressee_kind, addressee_object_id, addressee_version_id, reply_to_message_id, visibility,
			originated_at, created_at, seq, 0
		FROM messages WHERE id = ? AND session_id = ?
		UNION ALL
		SELECT m.id, m.session_id, m.parent_message_id, m.role, m.status, m.run_id,
			m.speaker_kind, m.speaker_object_id, m.speaker_version_id, m.participant_instance_id, m.speaker_snapshot_json,
			m.addressee_kind, m.addressee_object_id, m.addressee_version_id, m.reply_to_message_id, m.visibility,
			m.originated_at, m.created_at, m.seq, c.depth + 1
		FROM messages m JOIN chain c
		  ON m.id = c.parent_message_id AND m.session_id = c.session_id
		WHERE c.depth < 500
	)
	SELECT id, session_id, parent_message_id, role, status, run_id,
		speaker_kind, speaker_object_id, speaker_version_id, participant_instance_id, speaker_snapshot_json,
		addressee_kind, addressee_object_id, addressee_version_id, reply_to_message_id, visibility,
		originated_at, created_at, seq
	FROM chain ORDER BY depth DESC`

	rows, err := r.DB.QueryContext(ctx, query, leafID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("lineage query: %w", err)
	}
	var messages []domain.Message
	for rows.Next() {
		var message domain.Message
		var createdAt, speakerSnapshot string
		var runID, speakerObjectID, speakerVersionID, participantInstanceID sql.NullString
		var addresseeKind, addresseeObjectID, addresseeVersionID, replyToMessageID, originatedAt sql.NullString
		if err := rows.Scan(
			&message.ID, &message.SessionID, &message.ParentMessageID, &message.Role, &message.Status, &runID,
			&message.SpeakerKind, &speakerObjectID, &speakerVersionID, &participantInstanceID, &speakerSnapshot,
			&addresseeKind, &addresseeObjectID, &addresseeVersionID, &replyToMessageID, &message.Visibility,
			&originatedAt, &createdAt, &message.Seq,
		); err != nil {
			return nil, fmt.Errorf("scan lineage message: %w", err)
		}
		if runID.Valid {
			message.RunID = &runID.String
		}
		if speakerObjectID.Valid {
			message.SpeakerObjectID = &speakerObjectID.String
		}
		if speakerVersionID.Valid {
			message.SpeakerVersionID = &speakerVersionID.String
		}
		if participantInstanceID.Valid {
			message.ParticipantInstanceID = &participantInstanceID.String
		}
		message.SpeakerSnapshot = json.RawMessage(speakerSnapshot)
		if addresseeKind.Valid {
			message.AddresseeKind = &addresseeKind.String
		}
		if addresseeObjectID.Valid {
			message.AddresseeObjectID = &addresseeObjectID.String
		}
		if addresseeVersionID.Valid {
			message.AddresseeVersionID = &addresseeVersionID.String
		}
		if replyToMessageID.Valid {
			message.ReplyToMessageID = &replyToMessageID.String
		}
		if originatedAt.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, originatedAt.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse message originated_at: %w", parseErr)
			}
			message.OriginatedAt = &value
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

	const pageQuery = `WITH RECURSIVE page_chain(id,session_id,parent_message_id,role,status,run_id,
		speaker_kind,speaker_object_id,speaker_version_id,participant_instance_id,speaker_snapshot_json,
		addressee_kind,addressee_object_id,addressee_version_id,reply_to_message_id,visibility,
		originated_at,created_at,seq,depth,path) AS (
		SELECT id,session_id,parent_message_id,role,status,run_id,
			speaker_kind,speaker_object_id,speaker_version_id,participant_instance_id,speaker_snapshot_json,
			addressee_kind,addressee_object_id,addressee_version_id,reply_to_message_id,visibility,
			originated_at,created_at,seq,0,'|' || id || '|'
		FROM messages WHERE id=? AND session_id=?
		UNION ALL
		SELECT m.id,m.session_id,m.parent_message_id,m.role,m.status,m.run_id,
			m.speaker_kind,m.speaker_object_id,m.speaker_version_id,m.participant_instance_id,m.speaker_snapshot_json,
			m.addressee_kind,m.addressee_object_id,m.addressee_version_id,m.reply_to_message_id,m.visibility,
			m.originated_at,m.created_at,m.seq,c.depth+1,c.path || m.id || '|'
		FROM messages m JOIN page_chain c ON m.id=c.parent_message_id AND m.session_id=c.session_id
		WHERE c.depth < ? AND instr(c.path,'|' || m.id || '|')=0
	)
	SELECT c.id,c.session_id,c.parent_message_id,c.role,c.status,c.run_id,
		c.speaker_kind,c.speaker_object_id,c.speaker_version_id,c.participant_instance_id,c.speaker_snapshot_json,
		c.addressee_kind,c.addressee_object_id,c.addressee_version_id,c.reply_to_message_id,c.visibility,
		c.originated_at,c.created_at,c.seq,p.ordinal,p.block_kind,p.payload_json
	FROM page_chain c LEFT JOIN message_parts p ON p.message_id=c.id
	LEFT JOIN agent_runs ar ON ar.id=c.run_id
	WHERE ar.commit_format_version IS NULL OR ar.commit_format_version=1 OR c.visibility IN ('public','room_control')
	ORDER BY c.depth ASC,p.ordinal ASC`
	rows, err := r.DB.QueryContext(ctx, pageQuery, anchorID, sessionID, 500)
	if err != nil {
		return page, fmt.Errorf("message page query: %w", err)
	}
	defer rows.Close()

	newestFirst := make([]domain.Message, 0, limit+1)
	for rows.Next() {
		var id, rowSessionID, role, status, speakerKind, speakerSnapshot, visibility, createdAt string
		var parentID, runID, speakerObjectID, speakerVersionID, participantInstanceID sql.NullString
		var addresseeKind, addresseeObjectID, addresseeVersionID, replyToMessageID, originatedAt sql.NullString
		var ordinal sql.NullInt64
		var kind, payload sql.NullString
		var seq int64
		if err := rows.Scan(&id, &rowSessionID, &parentID, &role, &status, &runID,
			&speakerKind, &speakerObjectID, &speakerVersionID, &participantInstanceID, &speakerSnapshot,
			&addresseeKind, &addresseeObjectID, &addresseeVersionID, &replyToMessageID, &visibility,
			&originatedAt, &createdAt, &seq, &ordinal, &kind, &payload); err != nil {
			return page, fmt.Errorf("scan message page: %w", err)
		}
		if len(newestFirst) == 0 || newestFirst[len(newestFirst)-1].ID != id {
			created, parseErr := time.Parse(time.RFC3339Nano, createdAt)
			if parseErr != nil {
				return page, fmt.Errorf("parse message timestamp: %w", parseErr)
			}
			message := domain.Message{ID: id, SessionID: rowSessionID, Role: role, Status: status,
				SpeakerKind: domain.SpeakerKind(speakerKind), SpeakerSnapshot: json.RawMessage(speakerSnapshot),
				Visibility: domain.MessageVisibility(visibility), Parts: []domain.ContentBlock{}, CreatedAt: created, Seq: seq}
			if parentID.Valid {
				message.ParentMessageID = &parentID.String
			}
			if runID.Valid {
				message.RunID = &runID.String
			}
			if speakerObjectID.Valid {
				message.SpeakerObjectID = &speakerObjectID.String
			}
			if speakerVersionID.Valid {
				message.SpeakerVersionID = &speakerVersionID.String
			}
			if participantInstanceID.Valid {
				message.ParticipantInstanceID = &participantInstanceID.String
			}
			if addresseeKind.Valid {
				message.AddresseeKind = &addresseeKind.String
			}
			if addresseeObjectID.Valid {
				message.AddresseeObjectID = &addresseeObjectID.String
			}
			if addresseeVersionID.Valid {
				message.AddresseeVersionID = &addresseeVersionID.String
			}
			if replyToMessageID.Valid {
				message.ReplyToMessageID = &replyToMessageID.String
			}
			if originatedAt.Valid {
				originated, parseErr := time.Parse(time.RFC3339Nano, originatedAt.String)
				if parseErr != nil {
					return page, fmt.Errorf("parse message originated_at: %w", parseErr)
				}
				message.OriginatedAt = &originated
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

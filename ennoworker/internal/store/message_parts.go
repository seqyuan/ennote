package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

func loadMessageParts(ctx context.Context, db queryer, messageID string) ([]domain.ContentBlock, error) {
	rows, err := db.QueryContext(ctx, `SELECT block_kind, payload_json FROM message_parts
		WHERE message_id = ? ORDER BY ordinal`, messageID)
	if err != nil {
		return nil, fmt.Errorf("load message parts: %w", err)
	}
	defer rows.Close()
	var blocks []domain.ContentBlock
	for rows.Next() {
		var kind domain.ContentKind
		var payloadText string
		if err := rows.Scan(&kind, &payloadText); err != nil {
			return nil, fmt.Errorf("scan message part: %w", err)
		}
		block, err := decodeContentBlock(kind, json.RawMessage(payloadText))
		if err != nil {
			return nil, fmt.Errorf("decode %s message part: %w", kind, err)
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

func insertMessageParts(ctx context.Context, tx *sql.Tx, messageID string, blocks []domain.ContentBlock) error {
	for index, block := range blocks {
		payload, err := encodeContentBlock(block)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_parts
			(id, message_id, ordinal, block_kind, payload_json) VALUES (?, ?, ?, ?, ?)`,
			uuid.NewString(), messageID, index, block.Kind, string(payload)); err != nil {
			return fmt.Errorf("insert message part: %w", err)
		}
	}
	return nil
}

func encodeContentBlock(block domain.ContentBlock) (json.RawMessage, error) {
	switch block.Kind {
	case domain.ContentText, domain.ContentThinking:
		return json.Marshal(map[string]string{"text": block.Text})
	case domain.ContentToolCall:
		if block.ToolCall == nil {
			return nil, fmt.Errorf("tool call block is missing payload")
		}
		arguments := block.ToolCall.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		if !json.Valid(arguments) {
			return nil, fmt.Errorf("tool call arguments are not valid JSON")
		}
		return json.Marshal(struct {
			ID                string          `json:"id"`
			Name              string          `json:"name"`
			Arguments         json.RawMessage `json:"arguments"`
			ArgumentsFragment string          `json:"argumentsFragment,omitempty"`
			Partial           bool            `json:"partial,omitempty"`
		}{block.ToolCall.ID, block.ToolCall.Name, arguments, block.ToolCall.ArgumentsFragment, block.ToolCall.Partial})
	case domain.ContentToolResult:
		if block.ToolResult == nil {
			return nil, fmt.Errorf("tool result block is missing payload")
		}
		for _, ref := range block.ToolResult.Artifacts {
			if ref.ArtifactID == "" || ref.Name == "" || ref.Kind == "" || ref.MIMEType == "" || ref.SHA256 == "" {
				return nil, fmt.Errorf("tool result contains incomplete artifact reference")
			}
		}
		return json.Marshal(struct {
			ToolCallID string                     `json:"toolCallId"`
			ToolName   string                     `json:"toolName"`
			Content    string                     `json:"content"`
			IsError    bool                       `json:"isError"`
			Artifacts  []domain.ArtifactReference `json:"artifacts,omitempty"`
		}{block.ToolResult.ToolCallID, block.ToolResult.ToolName, block.ToolResult.Content,
			block.ToolResult.IsError, block.ToolResult.Artifacts})
	case domain.ContentImage:
		if block.Image == nil || block.Image.ArtifactID == "" {
			return nil, fmt.Errorf("image block is missing artifact reference")
		}
		return json.Marshal(block.Image)
	case domain.ContentImageDescription:
		if block.ImageDescription == nil || block.ImageDescription.ArtifactID == "" {
			return nil, fmt.Errorf("image description block is missing payload")
		}
		return json.Marshal(block.ImageDescription)
	case domain.ContentRoomControl:
		if block.RoomControl == nil || block.RoomControl.Action == "" || block.RoomControl.ParticipantInstanceID == "" ||
			block.RoomControl.ObjectID == "" || block.RoomControl.ObjectVersionID == "" || !json.Valid(block.RoomControl.DisplaySnapshot) {
			return nil, fmt.Errorf("room control block is missing participant identity")
		}
		return json.Marshal(block.RoomControl)
	default:
		return nil, fmt.Errorf("unsupported content block kind: %s", block.Kind)
	}
}

func decodeContentBlock(kind domain.ContentKind, payload json.RawMessage) (domain.ContentBlock, error) {
	block := domain.ContentBlock{Kind: kind}
	switch kind {
	case domain.ContentText, domain.ContentThinking:
		var value struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return block, err
		}
		block.Text = value.Text
	case domain.ContentToolCall:
		var call struct {
			ID                string          `json:"id"`
			Name              string          `json:"name"`
			Arguments         json.RawMessage `json:"arguments"`
			ArgumentsFragment string          `json:"argumentsFragment"`
			Partial           bool            `json:"partial"`
		}
		if err := json.Unmarshal(payload, &call); err != nil {
			return block, err
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		if !json.Valid(call.Arguments) {
			return block, fmt.Errorf("invalid tool call arguments")
		}
		block.ToolCall = &domain.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments,
			ArgumentsFragment: call.ArgumentsFragment, Partial: call.Partial}
	case domain.ContentToolResult:
		var result domain.ToolResult
		var value struct {
			ToolCallID string                     `json:"toolCallId"`
			ToolName   string                     `json:"toolName"`
			Content    string                     `json:"content"`
			IsError    bool                       `json:"isError"`
			Artifacts  []domain.ArtifactReference `json:"artifacts"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return block, err
		}
		for _, ref := range value.Artifacts {
			if ref.ArtifactID == "" || ref.Name == "" || ref.Kind == "" || ref.MIMEType == "" || ref.SHA256 == "" {
				return block, fmt.Errorf("tool result contains incomplete artifact reference")
			}
		}
		result = domain.ToolResult{ToolCallID: value.ToolCallID, ToolName: value.ToolName,
			Content: value.Content, IsError: value.IsError, Artifacts: value.Artifacts}
		block.ToolResult = &result
	case domain.ContentImage:
		var image domain.ImageRef
		if err := json.Unmarshal(payload, &image); err != nil {
			return block, err
		}
		if image.ArtifactID == "" {
			return block, fmt.Errorf("image artifact id is required")
		}
		block.Image = &image
	case domain.ContentImageDescription:
		var description domain.DerivedImageDescription
		if err := json.Unmarshal(payload, &description); err != nil {
			return block, err
		}
		block.ImageDescription = &description
	case domain.ContentRoomControl:
		var control domain.RoomControl
		if err := json.Unmarshal(payload, &control); err != nil {
			return block, err
		}
		if control.Action == "" || control.ParticipantInstanceID == "" || control.ObjectID == "" ||
			control.ObjectVersionID == "" || !json.Valid(control.DisplaySnapshot) {
			return block, fmt.Errorf("invalid room control participant identity")
		}
		block.RoomControl = &control
	default:
		return block, fmt.Errorf("unsupported content block kind: %s", kind)
	}
	return block, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

package domain

import (
	"encoding/json"
	"time"
)

// Project represents a logical research project.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProjectWorkspace binds a project to a host directory.
type ProjectWorkspace struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Kind            string    `json:"kind"`
	HostPath        string    `json:"hostPath"`
	VirtualPath     string    `json:"virtualPath"`
	Status          string    `json:"status"`
	PathFingerprint string    `json:"pathFingerprint"`
	CreatedAt       time.Time `json:"createdAt"`
}

type CreateProjectInput struct {
	Name        string
	Description string
	HostPath    string
}

type SessionMode string

const (
	SessionModeHosted SessionMode = "hosted"
	SessionModeRoom   SessionMode = "room"
)

type SpeakerKind string

const (
	SpeakerUser     SpeakerKind = "user"
	SpeakerHost     SpeakerKind = "host"
	SpeakerRole     SpeakerKind = "role"
	SpeakerWorkflow SpeakerKind = "workflow"
	SpeakerRoom     SpeakerKind = "room"
	SpeakerSystem   SpeakerKind = "system"
)

type MessageVisibility string

const (
	VisibilityPublic          MessageVisibility = "public"
	VisibilityPrivate         MessageVisibility = "private"
	VisibilityRoomControl     MessageVisibility = "room_control"
	VisibilityLegacyExecution MessageVisibility = "legacy_execution"
)

type Session struct {
	ID                        string      `json:"id"`
	ProjectID                 string      `json:"projectId"`
	Title                     string      `json:"title"`
	Status                    string      `json:"status"`
	Mode                      SessionMode `json:"mode"`
	ActiveLeafMessageID       *string     `json:"activeLeafMessageId,omitempty"`
	ActiveBranchID            *string     `json:"activeBranchId,omitempty"`
	DefaultAgentProfileID     *string     `json:"defaultAgentProfileId,omitempty"`
	DefaultModelProfileID     *string     `json:"defaultModelProfileId,omitempty"`
	CompactionPolicyProfileID *string     `json:"compactionPolicyProfileId,omitempty"`
	SourceSessionID           *string     `json:"sourceSessionId,omitempty"`
	SourceMessageID           *string     `json:"sourceMessageId,omitempty"`
	CreatedAt                 time.Time   `json:"createdAt"`
	UpdatedAt                 time.Time   `json:"updatedAt"`
}

type CreateSessionInput struct {
	ProjectID                 string
	Title                     string
	DefaultAgentProfileID     *string
	DefaultModelProfileID     *string
	CompactionPolicyProfileID *string
	SourceSessionID           *string
	SourceMessageID           *string
}

type Message struct {
	ID                    string            `json:"id"`
	SessionID             string            `json:"sessionId"`
	ParentMessageID       *string           `json:"parentMessageId,omitempty"`
	Role                  string            `json:"role"`
	Status                string            `json:"status"`
	RunID                 *string           `json:"runId,omitempty"`
	SpeakerKind           SpeakerKind       `json:"speakerKind"`
	SpeakerObjectID       *string           `json:"speakerObjectId,omitempty"`
	SpeakerVersionID      *string           `json:"speakerVersionId,omitempty"`
	ParticipantInstanceID *string           `json:"participantInstanceId,omitempty"`
	SpeakerSnapshot       json.RawMessage   `json:"speakerSnapshot"`
	AddresseeKind         *string           `json:"addresseeKind,omitempty"`
	AddresseeObjectID     *string           `json:"addresseeObjectId,omitempty"`
	AddresseeVersionID    *string           `json:"addresseeVersionId,omitempty"`
	ReplyToMessageID      *string           `json:"replyToMessageId,omitempty"`
	Visibility            MessageVisibility `json:"visibility"`
	OriginatedAt          *time.Time        `json:"originatedAt,omitempty"`
	Parts                 []ContentBlock    `json:"parts"`
	CreatedAt             time.Time         `json:"createdAt"`
	// Seq is the session-monotonic message sequence, assigned at insert time
	// (MAX(seq)+1 within the session) and used for gap detection / consecutive
	// assertions on the client. Legacy rows backfilled once read Seq = 0.
	Seq int64 `json:"seq"`
}

type SessionBranch struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"sessionId"`
	ParentBranchID *string   `json:"parentBranchId,omitempty"`
	ForkMessageID  *string   `json:"forkMessageId,omitempty"`
	LeafMessageID  *string   `json:"leafMessageId,omitempty"`
	Label          string    `json:"label"`
	MessageCount   int       `json:"messageCount"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type BranchNavigation struct {
	Session  Session         `json:"session"`
	Branches []SessionBranch `json:"branches"`
}

type SessionLineage struct {
	Messages []Message `json:"messages"`
}

type MessagePage struct {
	Messages            []Message
	HasMore             bool
	NextBeforeMessageID string
}

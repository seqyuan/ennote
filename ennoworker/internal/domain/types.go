package domain

import "time"

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

type Session struct {
	ID                        string    `json:"id"`
	ProjectID                 string    `json:"projectId"`
	Title                     string    `json:"title"`
	Status                    string    `json:"status"`
	ActiveLeafMessageID       *string   `json:"activeLeafMessageId,omitempty"`
	ActiveBranchID            *string   `json:"activeBranchId,omitempty"`
	DefaultAgentProfileID     *string   `json:"defaultAgentProfileId,omitempty"`
	DefaultModelProfileID     *string   `json:"defaultModelProfileId,omitempty"`
	CompactionPolicyProfileID *string   `json:"compactionPolicyProfileId,omitempty"`
	SourceSessionID           *string   `json:"sourceSessionId,omitempty"`
	SourceMessageID           *string   `json:"sourceMessageId,omitempty"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
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
	ID              string         `json:"id"`
	SessionID       string         `json:"sessionId"`
	ParentMessageID *string        `json:"parentMessageId,omitempty"`
	Role            string         `json:"role"`
	Status          string         `json:"status"`
	RunID           *string        `json:"runId,omitempty"`
	Parts           []ContentBlock `json:"parts"`
	CreatedAt       time.Time      `json:"createdAt"`
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

package domain

import "time"

type QueuedInputKind string

const (
	QueuedInputSteer    QueuedInputKind = "steer"
	QueuedInputFollowUp QueuedInputKind = "follow_up"
)

type QueueMode string

const (
	QueueOneAtATime QueueMode = "one-at-a-time"
	QueueAll        QueueMode = "all"
)

type QueuedInput struct {
	ID              string          `json:"id"`
	RunID           string          `json:"runId"`
	SessionID       string          `json:"sessionId"`
	ClientRequestID string          `json:"clientRequestId"`
	Seq             int64           `json:"seq"`
	Kind            QueuedInputKind `json:"kind"`
	Text            string          `json:"text"`
	Status          string          `json:"status"`
	CreatedAt       time.Time       `json:"createdAt"`
	InjectedAt      *time.Time      `json:"injectedAt,omitempty"`
}

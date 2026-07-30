package domain

import "time"

const (
	ArtifactKindImage      = "image"
	ArtifactKindTable      = "table"
	ArtifactKindStaticHTML = "static_html"
	ArtifactKindText       = "text"
	ArtifactKindFile       = "file"
)

type ArtifactReference struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	MIMEType   string `json:"mimeType"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

type Artifact struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"projectId"`
	SessionID           string    `json:"sessionId,omitempty"`
	MessageID           string    `json:"messageId,omitempty"`
	RunID               string    `json:"runId,omitempty"`
	Name                string    `json:"name"`
	Kind                string    `json:"kind"`
	MIMEType            string    `json:"mimeType"`
	StoragePath         string    `json:"-"`
	SizeBytes           int64     `json:"sizeBytes"`
	SHA256              string    `json:"sha256"`
	Width               int       `json:"width,omitempty"`
	Height              int       `json:"height,omitempty"`
	SourceToolCallID    string    `json:"sourceToolCallId,omitempty"`
	SourceKind          string    `json:"sourceKind,omitempty"`
	SourceWorkspacePath string    `json:"-"`
	RetentionClass      string    `json:"retentionClass"`
	CreatedAt           time.Time `json:"createdAt"`
}

func (a Artifact) Reference() ArtifactReference {
	return ArtifactReference{ArtifactID: a.ID, Name: a.Name, Kind: a.Kind, MIMEType: a.MIMEType,
		SizeBytes: a.SizeBytes, SHA256: a.SHA256, Width: a.Width, Height: a.Height}
}

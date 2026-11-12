package domain

import "time"

const (
	ArtifactKindImage      = "image"
	ArtifactKindTable      = "table"
	ArtifactKindStaticHTML = "static_html"
	// ArtifactKindWidget is a self-contained HTML document the client renders in an
	// isolated, script-enabled frame. It is never inferred: an .html file stays
	// ArtifactKindStaticHTML unless the publisher asks for a widget by name, because
	// inference would silently add script execution to files that never had it.
	ArtifactKindWidget = "widget"
	ArtifactKindText   = "text"
	ArtifactKindFile   = "file"
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
	// PreviewDrops names the kinds of content the static HTML preview will
	// strip (svg, script, an inline on* handler, ...). It is `json:"-"` because
	// it is a hint derived from stored metadata, not part of the artifact
	// contract: GET /artifacts/{id} keeps its exact shape, the web client learns
	// about drops from the preview document itself, and only the publishing tool
	// and the preview need the list on the server side.
	PreviewDrops []string `json:"-"`
	// PreviewDropsTotal is how many kinds were found before PreviewDrops was
	// capped, so a notice can say "and 3 more" instead of stating a total that
	// is only the part it had room for.
	PreviewDropsTotal int `json:"-"`
}

func (a Artifact) Reference() ArtifactReference {
	return ArtifactReference{ArtifactID: a.ID, Name: a.Name, Kind: a.Kind, MIMEType: a.MIMEType,
		SizeBytes: a.SizeBytes, SHA256: a.SHA256, Width: a.Width, Height: a.Height}
}

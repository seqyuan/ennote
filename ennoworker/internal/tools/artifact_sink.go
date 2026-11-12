package tools

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ArtifactSink struct {
	Service   *artifacts.Service
	ProjectID string
	SessionID string
	RunID     string
}

func (s *ArtifactSink) Publish(ctx context.Context, toolCallID, name, sourceKind, workspacePath string,
	source io.Reader) (domain.ArtifactReference, error) {
	artifact, err := s.PublishAs(ctx, toolCallID, name, sourceKind, workspacePath, "", source)
	if err != nil {
		return domain.ArtifactReference{}, err
	}
	return artifact.Reference(), nil
}

// PublishAs is Publish with an asserted kind. Only `publish_artifact` asks for one;
// command output and every other sink caller keeps the inferred behaviour.
//
// It returns the stored artifact rather than its reference because publish_artifact
// also has to tell the model what the preview will drop, and that hint is not part
// of ArtifactReference (which is the message contract). The reference stays the
// only thing the other callers see, through Publish.
func (s *ArtifactSink) PublishAs(ctx context.Context, toolCallID, name, sourceKind, workspacePath, kind string,
	source io.Reader) (*domain.Artifact, error) {
	if s == nil || s.Service == nil {
		return nil, fmt.Errorf("artifact service is unavailable")
	}
	artifact, err := s.Service.Store(ctx, artifacts.PublishInput{
		ProjectID: s.ProjectID, SessionID: s.SessionID, RunID: s.RunID, ToolCallID: toolCallID,
		Name: name, SourceKind: sourceKind, SourceWorkspacePath: workspacePath, RetentionClass: "project",
		Kind: kind,
	}, source)
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

func describeArtifacts(references []domain.ArtifactReference) string {
	var output strings.Builder
	for index, reference := range references {
		if index > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "published artifact %q (id=%s, kind=%s, mime=%s, bytes=%d, sha256=%s)",
			reference.Name, reference.ArtifactID, reference.Kind, reference.MIMEType,
			reference.SizeBytes, reference.SHA256)
	}
	return output.String()
}

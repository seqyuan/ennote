package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ValidateSubmitResult parses and validates the delegated child terminal
// contract. It is kept in domain so both the tool schema boundary and Agent
// Loop interception use exactly the same validation without a package cycle.
func ValidateSubmitResult(arguments json.RawMessage) (*SubmitResult, error) {
	var result SubmitResult
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid submit_result arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid submit_result arguments: exactly one JSON object is required")
	}
	switch result.Status {
	case SubmitCompleted, SubmitBlocked, SubmitNeedsInput:
	default:
		return nil, fmt.Errorf("submit_result status must be completed|blocked|needs_input")
	}
	if len(result.Summary) == 0 || len(result.Summary) > 4096 {
		return nil, fmt.Errorf("submit_result summary must be 1..4096 bytes")
	}
	if len(result.ArtifactRefs) > 32 {
		return nil, fmt.Errorf("submit_result artifactRefs exceeds 32")
	}
	seenArtifacts := make(map[string]struct{}, len(result.ArtifactRefs))
	for index, reference := range result.ArtifactRefs {
		if strings.TrimSpace(reference.ArtifactID) == "" || strings.TrimSpace(reference.Name) == "" ||
			strings.TrimSpace(reference.Kind) == "" || strings.TrimSpace(reference.MIMEType) == "" ||
			strings.TrimSpace(reference.SHA256) == "" {
			return nil, fmt.Errorf("submit_result artifactRefs[%d] is incomplete", index)
		}
		if _, exists := seenArtifacts[reference.ArtifactID]; exists {
			return nil, fmt.Errorf("submit_result artifact %s is duplicated", reference.ArtifactID)
		}
		seenArtifacts[reference.ArtifactID] = struct{}{}
	}
	if len(result.Payload) > 0 {
		if !json.Valid(result.Payload) {
			return nil, fmt.Errorf("submit_result payload is not valid JSON")
		}
		trimmed := bytes.TrimSpace(result.Payload)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("submit_result payload must be a JSON object")
		}
	}
	if len(result.Payload) > 65536 {
		return nil, fmt.Errorf("submit_result payload exceeds 64 KiB")
	}
	return &result, nil
}

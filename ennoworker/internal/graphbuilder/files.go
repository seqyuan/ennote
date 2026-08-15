package graphbuilder

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
)

type fileThreadMetadata struct {
	SchemaVersion  int       `json:"schemaVersion"`
	GraphID        string    `json:"graphId"`
	ModelProfileID string    `json:"modelProfileId"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (s *Service) getFileThread(graphID string) (*Thread, error) {
	if s.Sources == nil {
		return nil, fmt.Errorf("Graph source store is unavailable")
	}
	if _, _, err := s.Sources.ReadGraph(graphID); err != nil {
		return nil, err
	}
	builderDir := filepath.Join(s.Sources.GraphsDir(), graphID, "builder")
	thread := &Thread{GraphID: graphID, Messages: []Message{}}
	metadataPath := filepath.Join(builderDir, "thread.json")
	if contents, err := os.ReadFile(metadataPath); err == nil {
		var metadata fileThreadMetadata
		if err := json.Unmarshal(contents, &metadata); err != nil {
			return nil, fmt.Errorf("decode Graph Builder thread: %w", err)
		}
		if metadata.SchemaVersion != 1 || metadata.GraphID != graphID {
			return nil, fmt.Errorf("Graph Builder thread identity is invalid")
		}
		thread.ModelProfileID = metadata.ModelProfileID
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	messagesPath := filepath.Join(builderDir, "messages.jsonl")
	if file, err := os.Open(messagesPath); err == nil {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var message Message
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				file.Close()
				return nil, fmt.Errorf("decode Graph Builder message: %w", err)
			}
			if message.GraphID != graphID || message.Ordinal != len(thread.Messages)+1 ||
				(message.Role != "user" && message.Role != "assistant") {
				file.Close()
				return nil, fmt.Errorf("Graph Builder message sequence is invalid")
			}
			thread.Messages = append(thread.Messages, message)
		}
		if err := scanner.Err(); err != nil {
			file.Close()
			return nil, err
		}
		file.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	proposal, err := s.pendingFileProposal(graphID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	thread.Proposal = proposal
	return thread, nil
}

func (s *Service) sendFile(ctx context.Context, graphID, modelProfileID, instruction string) (*Thread, error) {
	instruction = strings.TrimSpace(instruction)
	modelProfileID = strings.TrimSpace(modelProfileID)
	if instruction == "" || modelProfileID == "" {
		return nil, fmt.Errorf("modelProfileId and instruction are required")
	}
	if s.Completer == nil {
		return nil, fmt.Errorf("Graph Builder model service is unavailable")
	}
	document, digest, err := s.Sources.ReadGraph(graphID)
	if err != nil {
		return nil, err
	}
	builderDir := filepath.Join(s.Sources.GraphsDir(), graphID, "builder")
	if err := os.MkdirAll(filepath.Join(builderDir, "proposals"), 0o700); err != nil {
		return nil, err
	}
	thread, err := s.getFileThread(graphID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	metadata := fileThreadMetadata{SchemaVersion: 1, GraphID: graphID, ModelProfileID: modelProfileID,
		CreatedAt: now, UpdatedAt: now}
	if thread.ModelProfileID != "" {
		if contents, readErr := os.ReadFile(filepath.Join(builderDir, "thread.json")); readErr == nil {
			var existing fileThreadMetadata
			if json.Unmarshal(contents, &existing) == nil {
				metadata.CreatedAt = existing.CreatedAt
			}
		}
	}
	if err := writeJSONAtomic(filepath.Join(builderDir, "thread.json"), metadata); err != nil {
		return nil, err
	}
	if err := appendFileMessage(filepath.Join(builderDir, "messages.jsonl"), Message{
		ID: uuid.NewString(), GraphID: graphID, Ordinal: len(thread.Messages) + 1,
		Role: "user", Content: instruction, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	graphJSON, _ := json.MarshalIndent(document, "", "  ")
	response, err := s.Completer.Complete(ctx, modelProfileID, skillPrompt,
		"Current Graph:\n"+string(graphJSON)+"\n\nUser instruction:\n"+instruction)
	if err != nil {
		return nil, err
	}
	var output struct {
		Message    string      `json:"message"`
		Operations []Operation `json:"operations"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); err != nil {
		return nil, fmt.Errorf("Graph Builder returned invalid JSON: %w", err)
	}
	if strings.TrimSpace(output.Message) == "" {
		return nil, fmt.Errorf("Graph Builder response requires a message")
	}
	if err := appendFileMessage(filepath.Join(builderDir, "messages.jsonl"), Message{
		ID: uuid.NewString(), GraphID: graphID, Ordinal: len(thread.Messages) + 2,
		Role: "assistant", Content: output.Message, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	if err := s.supersedeFileProposals(graphID); err != nil {
		return nil, err
	}
	if len(output.Operations) > 0 {
		proposal := Proposal{
			ID: uuid.NewString(), GraphID: graphID, BaseDigest: digest,
			Operations: output.Operations, Summary: output.Message, Status: "pending",
			Diagnostics: validateOperations(document, output.Operations), CreatedAt: time.Now().UTC(),
		}
		if err := writeJSONAtomic(filepath.Join(builderDir, "proposals", proposal.ID+".json"), proposal); err != nil {
			return nil, err
		}
	}
	return s.getFileThread(graphID)
}

func (s *Service) applyFile(_ context.Context, graphID, proposalID string) (*graphsource.Document, string, error) {
	proposal, err := s.pendingFileProposal(graphID)
	if err != nil {
		return nil, "", err
	}
	if proposal.ID != proposalID {
		return nil, "", fmt.Errorf("proposal is no longer pending")
	}
	if len(proposal.Diagnostics) > 0 {
		return nil, "", fmt.Errorf("proposal is invalid: %s", proposal.Diagnostics[0])
	}
	document, digest, err := s.Sources.UpdateGraph(graphID, proposal.BaseDigest, func(document *graphsource.Document) error {
		applyOperations(document, proposal.Operations)
		return nil
	})
	if err != nil {
		return nil, digest, err
	}
	proposal.Status = "applied"
	if err := writeJSONAtomic(filepath.Join(s.Sources.GraphsDir(), graphID, "builder", "proposals", proposal.ID+".json"), proposal); err != nil {
		return nil, digest, err
	}
	return document, digest, nil
}

func (s *Service) pendingFileProposal(graphID string) (*Proposal, error) {
	directory := filepath.Join(s.Sources.GraphsDir(), graphID, "builder", "proposals")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	proposals := make([]Proposal, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var proposal Proposal
		if err := json.Unmarshal(contents, &proposal); err != nil {
			return nil, err
		}
		if proposal.GraphID != graphID || proposal.ID+".json" != entry.Name() {
			return nil, fmt.Errorf("Graph Builder proposal identity is invalid")
		}
		if proposal.Status == "pending" {
			proposals = append(proposals, proposal)
		}
	}
	if len(proposals) == 0 {
		return nil, os.ErrNotExist
	}
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].CreatedAt.After(proposals[j].CreatedAt) })
	return &proposals[0], nil
}

func (s *Service) supersedeFileProposals(graphID string) error {
	proposal, err := s.pendingFileProposal(graphID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	proposal.Status = "superseded"
	return writeJSONAtomic(filepath.Join(s.Sources.GraphsDir(), graphID, "builder", "proposals", proposal.ID+".json"), proposal)
}

func appendFileMessage(path string, message Message) error {
	contents, err := json.Marshal(message)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writeJSONAtomic(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".builder-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

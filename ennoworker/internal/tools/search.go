package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type GrepTool struct {
	Jail         *workspace.Jail
	MaxResults   int
	MaxFileBytes int64
}
type FindTool struct {
	Jail       *workspace.Jail
	MaxResults int
}

func (t *GrepTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }
func (t *FindTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *GrepTool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2}
}
func (t *FindTool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2}
}

func (t *GrepTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "grep", Description: "Search text files under /workspace with a regular expression", Parameters: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)}
}
func (t *FindTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "find", Description: "Find files under /workspace by glob pattern", Parameters: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)}
}

func (t *GrepTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid grep arguments: %w", err)), nil
	}
	expression, err := regexp.Compile(args.Pattern)
	if err != nil {
		return errorResult(call, fmt.Errorf("invalid regular expression: %w", err)), nil
	}
	root, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	limit := t.MaxResults
	if limit <= 0 {
		limit = 200
	}
	maxFile := t.MaxFileBytes
	if maxFile <= 0 {
		maxFile = 2 << 20
	}
	var matches []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFile {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), int(maxFile))
		line := 0
		for scanner.Scan() {
			line++
			if expression.MatchString(scanner.Text()) {
				display, _ := t.Jail.DisplayPath(path)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", display, line, scanner.Text()))
				if len(matches) >= limit {
					file.Close()
					return fs.SkipAll
				}
			}
		}
		file.Close()
		return nil
	})
	if walkErr != nil && !errorsIsSkipAll(walkErr) {
		return errorResult(call, walkErr), nil
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: strings.Join(matches, "\n")}, nil
}

func (t *FindTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid find arguments: %w", err)), nil
	}
	root, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	limit := t.MaxResults
	if limit <= 0 {
		limit = 500
	}
	var matches []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		matched, err := filepath.Match(args.Pattern, entry.Name())
		if err != nil {
			return err
		}
		if !matched {
			matched, _ = filepath.Match(args.Pattern, filepath.ToSlash(relative))
		}
		if matched {
			display, _ := t.Jail.DisplayPath(path)
			matches = append(matches, display)
			if len(matches) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errorsIsSkipAll(walkErr) {
		return errorResult(call, walkErr), nil
	}
	sort.Strings(matches)
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: strings.Join(matches, "\n")}, nil
}

func errorsIsSkipAll(err error) bool { return err == fs.SkipAll }

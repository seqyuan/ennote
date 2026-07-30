package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const defaultCommandArtifactLimit = int64(50 << 20)

type boundedBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(value) > remaining {
			b.data = append(b.data, value[:remaining]...)
		} else {
			b.data = append(b.data, value...)
		}
	}
	if original > remaining {
		b.truncated = true
	}
	return original, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := string(b.data)
	if b.truncated {
		value += "\n[output truncated]"
	}
	return value
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

type outputCapture struct {
	mu       sync.Mutex
	preview  *boundedBuffer
	file     *os.File
	path     string
	limit    int64
	written  int64
	overflow bool
}

func newOutputCapture(runtimeDir, stream string, previewLimit int, artifactLimit int64) (*outputCapture, error) {
	if artifactLimit <= 0 {
		artifactLimit = defaultCommandArtifactLimit
	}
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(runtimeDir, "."+stream+"-*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return &outputCapture{preview: newBoundedBuffer(previewLimit), file: file, path: file.Name(), limit: artifactLimit}, nil
}

func (c *outputCapture) Write(value []byte) (int, error) {
	_, _ = c.preview.Write(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	original := len(value)
	if c.overflow {
		return original, nil
	}
	remaining := c.limit - c.written
	if int64(len(value)) > remaining {
		if remaining > 0 {
			if _, err := c.file.Write(value[:remaining]); err != nil {
				return 0, err
			}
			c.written += remaining
		}
		c.overflow = true
		return original, nil
	}
	written, err := c.file.Write(value)
	c.written += int64(written)
	if err != nil {
		return written, err
	}
	return original, nil
}

func (c *outputCapture) String() string { return c.preview.String() }

func (c *outputCapture) CloseAndPublish(ctx context.Context, sink *ArtifactSink, callID, stream string) (*domain.ArtifactReference, string, error) {
	c.mu.Lock()
	if c.file != nil {
		if err := c.file.Sync(); err != nil {
			c.mu.Unlock()
			return nil, "", err
		}
		if err := c.file.Close(); err != nil {
			c.mu.Unlock()
			return nil, "", err
		}
		c.file = nil
	}
	overflow := c.overflow
	path := c.path
	c.mu.Unlock()
	if !c.preview.Truncated() {
		return nil, "", nil
	}
	if overflow {
		return nil, fmt.Sprintf("[%s full output exceeded the %d-byte artifact limit and was not retained]", stream, c.limit), nil
	}
	if sink == nil {
		return nil, "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	name := filepath.Base(stream + ".txt")
	reference, err := sink.Publish(ctx, callID, name, "command_output", "", file)
	if err != nil {
		return nil, "", err
	}
	return &reference, fmt.Sprintf("[%s full output retained as artifact %s]", stream, reference.ArtifactID), nil
}

func collectOutputArtifacts(ctx context.Context, sink *ArtifactSink, callID string,
	stdout, stderr *outputCapture) ([]domain.ArtifactReference, []string, error) {
	var references []domain.ArtifactReference
	var notices []string
	for _, stream := range []struct {
		name    string
		capture *outputCapture
	}{{"stdout", stdout}, {"stderr", stderr}} {
		reference, notice, err := stream.capture.CloseAndPublish(ctx, sink, callID, stream.name)
		if err != nil {
			return references, notices, fmt.Errorf("retain %s: %w", stream.name, err)
		}
		if reference != nil {
			references = append(references, *reference)
		}
		if notice != "" {
			notices = append(notices, notice)
		}
	}
	return references, notices, nil
}

func (c *outputCapture) Cleanup() {
	c.mu.Lock()
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
	path := c.path
	c.mu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

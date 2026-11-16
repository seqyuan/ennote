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

// boundedBuffer keeps a bounded preview of one command stream: the first
// headMax bytes (the head) and, once the head is full, the LAST tailMax bytes (the
// tail). headMax+tailMax is the bound, so keeping the tail costs the head nothing
// but the middle of the output.
//
// The split exists because the verdict is at the tail: every test runner prints
// its pass/fail summary last, so a head-only preview made a passing suite read as
// unverifiable. The retained text marks both cuts (see the domain truncation
// markers), and a reader that only wants to know "was part of this dropped" can
// still ask Truncated.
type boundedBuffer struct {
	mu      sync.Mutex
	limit   int
	headMax int
	// head is the retained beginning of the stream.
	head []byte
	// tail is a ring of tailMax bytes holding the END of the stream. tailAt is
	// the next write position; once tailLen == tailMax it also points at the
	// oldest retained byte.
	tail    []byte
	tailMax int
	tailAt  int
	tailLen int
	// seen is every byte written, so Truncated can answer exactly: something was
	// dropped iff more arrived than the bound holds.
	seen int64
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit <= 0 {
		limit = 1 << 20
	}
	headMax := limit / 2
	return &boundedBuffer{limit: limit, headMax: headMax, tail: make([]byte, limit-headMax), tailMax: limit - headMax}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	b.seen += int64(original)
	if room := b.headMax - len(b.head); room > 0 {
		if len(value) <= room {
			b.head = append(b.head, value...)
			return original, nil
		}
		b.head = append(b.head, value[:room]...)
		value = value[room:]
	}
	b.writeTail(value)
	return original, nil
}

// writeTail appends value to the tail ring, dropping the oldest retained bytes
// when it does not fit.
func (b *boundedBuffer) writeTail(value []byte) {
	if len(value) >= b.tailMax {
		copy(b.tail, value[len(value)-b.tailMax:])
		b.tailAt = 0
		b.tailLen = b.tailMax
		return
	}
	copied := copy(b.tail[b.tailAt:], value)
	if copied < len(value) {
		copy(b.tail, value[copied:])
	}
	b.tailAt = (b.tailAt + len(value)) % b.tailMax
	if b.tailLen < b.tailMax {
		b.tailLen += len(value)
		if b.tailLen > b.tailMax {
			b.tailLen = b.tailMax
		}
	}
}

// retainedTail returns the tail bytes in output order.
func (b *boundedBuffer) retainedTail() []byte {
	if b.tailLen == 0 {
		return nil
	}
	if b.tailLen < b.tailMax {
		return b.tail[:b.tailLen]
	}
	ordered := make([]byte, 0, b.tailLen)
	ordered = append(ordered, b.tail[b.tailAt:]...)
	return append(ordered, b.tail[:b.tailAt]...)
}

// String renders the retained preview: the head, the two cut markers, the tail.
// The markers are added on top of the bound, exactly like the single marker this
// replaced.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated() {
		return string(b.head) + string(b.retainedTail())
	}
	return string(b.head) + "\n" + domain.OutputHeadTruncationMarker + "\n" +
		domain.OutputTailTruncationMarker + "\n" + string(b.retainedTail())
}

// truncated reports that the preview is not the whole stream.
func (b *boundedBuffer) truncated() bool {
	return b.seen > int64(b.limit)
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated()
}

type outputCapture struct {
	mu       sync.Mutex
	preview  *boundedBuffer
	file     *os.File
	path     string
	limit    int64
	written  int64
	overflow bool
	stream   string
	callID   string
	sink     domain.ToolOutputSink
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
	// Non-blocking live streaming to the tool's output sink (if attached).
	if c.sink != nil {
		c.sink.TryEmit(domain.ToolOutputUpdate{
			ToolCallID: c.callID,
			Stream:     c.stream,
			Data:       append([]byte(nil), value...),
		})
	}
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

// AttachSink wires a live streaming sink to the capture. Called once per
// capture before the command starts; never concurrent with Write.
func (c *outputCapture) AttachSink(callID, stream string, sink domain.ToolOutputSink) {
	c.callID = callID
	c.stream = stream
	c.sink = sink
}

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

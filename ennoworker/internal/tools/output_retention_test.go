package tools

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pytestPassSummary is the tail every test runner prints: the pass summary. It is
// the reason a head-only preview made a passing suite read as unverifiable.
const pytestPassSummary = "===== 40 passed, 2 skipped in 91.20s (0:01:31) =====\n"

// retained is the number of output bytes the buffer still holds.
func retained(buffer *boundedBuffer) int {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return len(buffer.head) + buffer.tailLen
}

// A verbose run is 3 MiB long and its verdict is in the last 2 KiB, so a
// head-only preview reports the opposite of what happened. The bounded buffer
// keeps both ends, inside the same total bound.
func TestBoundedBufferRetainsHeadAndTail(t *testing.T) {
	const limit = 64 << 10
	buffer := newBoundedBuffer(limit)
	body := strings.Repeat("tests/test_pipeline.py::test_align PASSED  [ 12%]\n", 3<<20/(47))
	output := body + pytestPassSummary
	require.Greater(t, len(output), 3<<20)

	_, err := buffer.Write([]byte(output))
	require.NoError(t, err)
	got := buffer.String()

	// The head is still there, unchanged and in place.
	assert.True(t, strings.HasPrefix(got, body[:1024]), "the retained head must still be the start of the output")
	// The tail is the true end of the output, so the summary survives.
	assert.True(t, strings.HasSuffix(got, pytestPassSummary), "the retained tail must still be the end of the output")
	// Both cuts are marked, distinctly.
	assert.Contains(t, got, domain.OutputHeadTruncationMarker)
	assert.Contains(t, got, domain.OutputTailTruncationMarker)
	assert.NotEqual(t, domain.OutputHeadTruncationMarker, domain.OutputTailTruncationMarker)
	// Every existing reader greps for this literal, so it has to survive.
	assert.Contains(t, got, "[output truncated]")
	assert.Less(t, strings.Index(got, domain.OutputHeadTruncationMarker),
		strings.Index(got, domain.OutputTailTruncationMarker), "the head marker closes the head and opens the tail")
	// The bound is unchanged: only the marker text is added on top of it.
	assert.LessOrEqual(t, retained(buffer), limit)
	assert.LessOrEqual(t, len(got), limit+2*len(domain.OutputHeadTruncationMarker)+16)
	assert.True(t, buffer.Truncated())
}

// The bound holds while the output streams in, not just once: a run that prints
// for minutes must not accumulate.
func TestBoundedBufferStaysBoundedWhileStreaming(t *testing.T) {
	const limit = 4096
	buffer := newBoundedBuffer(limit)
	chunk := []byte(strings.Repeat("abcdefgh", 512)) // 4 KiB per write
	for written := 0; written < 1<<20; written += len(chunk) {
		n, err := buffer.Write(chunk)
		require.NoError(t, err)
		require.Equal(t, len(chunk), n, "Write reports the whole input, not what it kept")
		require.LessOrEqual(t, retained(buffer), limit)
	}
	assert.True(t, buffer.Truncated())
}

// An output that fits is returned verbatim, with no markers and no cut.
func TestBoundedBufferKeepsShortOutputWhole(t *testing.T) {
	buffer := newBoundedBuffer(1024)
	output := "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"
	_, err := buffer.Write([]byte(output))
	require.NoError(t, err)
	assert.Equal(t, output, buffer.String())
	assert.False(t, buffer.Truncated())
	assert.True(t, retained(buffer) <= 1024)
}

// The markers have to describe the two cuts in the two directions, because a
// reader decides on the retained tail: the head marker closes the head, the tail
// marker opens the tail.
func TestTruncationMarkersDocumentTheTwoCuts(t *testing.T) {
	assert.Contains(t, domain.OutputHeadTruncationMarker, "[output truncated]")
	assert.Contains(t, domain.OutputTailTruncationMarker, "[output truncated]")
	assert.Contains(t, domain.OutputHeadTruncationMarker, "head")
	assert.Contains(t, domain.OutputTailTruncationMarker, "tail")
}

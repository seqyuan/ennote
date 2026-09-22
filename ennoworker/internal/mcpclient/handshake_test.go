package mcpclient

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestBoundInstructionsKeepsShortGuidanceVerbatim(t *testing.T) {
	guidance := "Use search before fetch."
	bounded, original := boundInstructions(guidance)
	assert.Equal(t, guidance, bounded)
	assert.Equal(t, len(guidance), original)

	empty, size := boundInstructions("")
	assert.Empty(t, empty)
	assert.Equal(t, 0, size)
}

func TestBoundInstructionsKeepsExactlyTheLimit(t *testing.T) {
	guidance := strings.Repeat("a", MaxInstructionBytes)
	bounded, original := boundInstructions(guidance)
	assert.Equal(t, guidance, bounded)
	assert.Equal(t, MaxInstructionBytes, original)
	assert.NotContains(t, bounded, "truncated")
}

// The marker is in-band on purpose: a reader of the frozen prompt must be able to
// tell that the text is not everything the server said.
func TestBoundInstructionsTruncatesWithAnExplicitMarker(t *testing.T) {
	guidance := strings.Repeat("a", MaxInstructionBytes) + strings.Repeat("b", 100)
	bounded, original := boundInstructions(guidance)

	assert.Equal(t, len(guidance), original)
	assert.Less(t, len(bounded), original, "the recorded guidance must be smaller than what was sent")
	assert.True(t, strings.HasPrefix(bounded, strings.Repeat("a", MaxInstructionBytes)))
	assert.Contains(t, bounded, "100 of")
	assert.Contains(t, bounded, "bytes omitted")
	assert.Greater(t, len(bounded), MaxInstructionBytes, "the marker itself is added, not counted in the budget")
}

// A naive byte slice can split a multi-byte rune; the recorded text must stay
// valid UTF-8 because it may enter a prompt.
func TestBoundInstructionsNeverSplitsARune(t *testing.T) {
	// Each rune is 3 bytes, so the limit lands mid-rune.
	guidance := strings.Repeat("生", MaxInstructionBytes) + "bi"
	bounded, original := boundInstructions(guidance)

	assert.Equal(t, len(guidance), original)
	assert.True(t, utf8.ValidString(bounded), "truncated guidance must stay valid UTF-8")
	assert.Contains(t, bounded, "bytes omitted")
}

func TestServerHandshakeDigestFollowsTheRecordedBytes(t *testing.T) {
	assert.Empty(t, ServerHandshake{}.InstructionDigest(), "no guidance has no identity")

	first := ServerHandshake{Instructions: "Search first."}.InstructionDigest()
	second := ServerHandshake{Instructions: "Fetch first."}.InstructionDigest()
	assert.NotEmpty(t, first)
	assert.NotEqual(t, first, second)
	assert.Equal(t, first, ServerHandshake{Instructions: "Search first."}.InstructionDigest())
}

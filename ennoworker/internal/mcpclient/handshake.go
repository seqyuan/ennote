package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"
)

// MaxInstructionBytes bounds one server's declared instructions. The value is
// enforced where the handshake is read, so no later stage has to re-guard against
// an unbounded server payload.
const MaxInstructionBytes = 4096

// ServerHandshake is the server's self-description from the initialize result:
// what revision it negotiated and the guidance it asks callers to follow.
//
// Instructions are untrusted external text. They are bounded here, at the only
// place they enter the process, and their origin is recorded so a reader can
// attribute them to one server at one binding revision.
type ServerHandshake struct {
	// ProtocolVersion is the revision the handshake negotiated: not what was
	// requested, and not a default. An unavailable server never negotiated one.
	ProtocolVersion string
	// Instructions is the server's usage guidance, truncated to
	// MaxInstructionBytes with an explicit marker when it was longer.
	Instructions string
	// InstructionBytes is the untruncated byte length of the server's guidance.
	InstructionBytes int
}

// InstructionDigest is the digest of exactly the recorded instruction bytes, or
// empty when the server declared none. It is the identity half of the pair a
// frozen Run records, so a later reader can tell whether two Runs saw the same
// guidance without comparing the text.
func (h ServerHandshake) InstructionDigest() string {
	if h.Instructions == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.Instructions))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// boundInstructions truncates guidance to the limit and appends an explicit
// marker naming how much was dropped.
//
// The marker is in-band on purpose. A reader of the frozen prompt must be able to
// tell that the text they see is not everything the server said, and the model
// must not be led to believe it received complete guidance. The marker's own
// bytes are not charged against the budget, so the reason is never itself
// truncated away.
func boundInstructions(raw string) (string, int) {
	original := len(raw)
	if original <= MaxInstructionBytes {
		return raw, original
	}
	// Drop any partial trailing rune: the result may enter a prompt, where
	// invalid UTF-8 is at best noise and at worst an encoding failure.
	head := raw[:MaxInstructionBytes]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	return head + fmt.Sprintf("\n[truncated: %d of %d bytes omitted]", original-len(head), original), original
}

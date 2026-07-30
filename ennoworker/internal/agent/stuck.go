package agent

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const (
	stuckWindow      = 16
	stuckRepeatLimit = 5
)

type stuckGuard struct {
	recent []string
}

func (g *stuckGuard) Restore(signatures []string) {
	g.recent = append(g.recent[:0], signatures...)
	if len(g.recent) > stuckWindow {
		g.recent = g.recent[len(g.recent)-stuckWindow:]
	}
}

func (g *stuckGuard) Snapshot() []string {
	return append([]string(nil), g.recent...)
}

func (g *stuckGuard) Repeated(calls []domain.ToolCall) bool {
	hash := sha256.New()
	for _, call := range calls {
		hash.Write([]byte(call.Name))
		hash.Write([]byte{0})
		hash.Write(call.Arguments)
		hash.Write([]byte{0xff})
	}
	signature := hex.EncodeToString(hash.Sum(nil))
	count := 1
	for _, previous := range g.recent {
		if previous == signature {
			count++
		}
	}
	g.recent = append(g.recent, signature)
	if len(g.recent) > stuckWindow {
		g.recent = g.recent[len(g.recent)-stuckWindow:]
	}
	return count >= stuckRepeatLimit
}

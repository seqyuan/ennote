package graphrun

import (
	"encoding/json"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
)

// ManifestDigest re-exports the flow manifest identity digest for tests and
// diagnostics.
func ManifestDigest(configDigest string, inputsJSON json.RawMessage) (string, error) {
	return agentflow.ManifestDigest(configDigest, inputsJSON)
}

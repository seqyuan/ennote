package globalsource

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// GraphFileKind is the on-disk shape of a graph.yaml, decided from its
// top-level keys before any parser sees the bytes. SourceKind reports only
// which filename exists; this is what tells a FlowDefinition apart from a
// legacy graphsource.Document. See docs/plans/2026-11-14-graph-unification-design.md §6.5.
type GraphFileKind string

const (
	GraphFileDefinition GraphFileKind = "definition"
	GraphFileLegacy     GraphFileKind = "legacy"
	GraphFileUnknown    GraphFileKind = "unknown"
)

// ClassifyGraphSource inspects only the top-level mapping keys.
//
//   - schemaVersion present and graph absent: a FlowDefinition.
//   - schema_version and graph present, schemaVersion absent: a legacy
//     graphsource.Document.
//   - anything else, including both schemaVersion and graph: an error with
//     kind GraphFileUnknown. A typo must not be retried against the other
//     parser.
func ClassifyGraphSource(data []byte) (GraphFileKind, error) {
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(false)
	if err := decoder.Decode(&root); err != nil {
		return GraphFileUnknown, fmt.Errorf("graph source is not a YAML document: %w", err)
	}
	mapping := &root
	if mapping.Kind == yaml.DocumentNode {
		if len(mapping.Content) != 1 {
			return GraphFileUnknown, fmt.Errorf("graph source is not a single YAML document")
		}
		mapping = mapping.Content[0]
	}
	if mapping.Kind != yaml.MappingNode {
		return GraphFileUnknown, fmt.Errorf("graph source is not a YAML mapping")
	}
	keys := map[string]bool{}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keys[mapping.Content[i].Value] = true
	}
	switch {
	case keys["schemaVersion"] && !keys["graph"]:
		return GraphFileDefinition, nil
	case keys["schema_version"] && keys["graph"] && !keys["schemaVersion"]:
		return GraphFileLegacy, nil
	default:
		return GraphFileUnknown, fmt.Errorf("graph source keys are neither a FlowDefinition nor a legacy graph document")
	}
}

package globalsource

import "testing"

func TestClassifyGraphSource(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want GraphFileKind
	}{
		{"definition", "schemaVersion: 1\nid: qc\ntasks: {}\n", GraphFileDefinition},
		{"legacy", "schema_version: 1\nid: qc\ngraph: {}\ntasks: {}\n", GraphFileLegacy},
		{"ambiguous", "schemaVersion: 1\ngraph: {}\n", GraphFileUnknown},
		{"empty", "", GraphFileUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyGraphSource([]byte(test.yaml))
			if test.want == GraphFileUnknown {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %q err=%v, want %q", got, err, test.want)
			}
		})
	}
}

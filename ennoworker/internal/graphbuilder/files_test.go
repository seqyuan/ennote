package graphbuilder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileBuilderPersistsThreadProposalAndApply(t *testing.T) {
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, _, err := sources.CreateGraphDefinition("rna-seq", "RNA-seq")
	require.NoError(t, err)
	service := &Service{
		Sources: sources,
		Completer: CompleteFunc(func(context.Context, string, string, string) (string, error) {
			return `{"message":"Add alignment","operations":[{"kind":"upsert_task","taskId":"align","task":{"name":"Align","model":"deepseek/deepseek-chat","goal":"Align reads"}},{"kind":"set_dependencies","taskId":"align","depends":[]}]}`, nil
		}),
	}

	thread, err := service.Send(context.Background(), "rna-seq", "deepseek/deepseek-chat", "Add alignment")
	require.NoError(t, err)
	require.Len(t, thread.Messages, 2)
	require.NotNil(t, thread.Proposal)
	assert.Equal(t, "pending", thread.Proposal.Status)
	builderDir := filepath.Join(sources.GraphsDir(), "rna-seq", "builder")
	require.FileExists(t, filepath.Join(builderDir, "messages.jsonl"))
	require.FileExists(t, filepath.Join(builderDir, "proposals", thread.Proposal.ID+".json"))

	definition, _, err := service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err)
	assert.Contains(t, definition.Tasks, "align")
	reloaded, err := service.GetThread(context.Background(), "rna-seq")
	require.NoError(t, err)
	assert.Nil(t, reloaded.Proposal)

	info, err := os.Stat(filepath.Join(builderDir, "messages.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// The apply rewrites graph.yaml from the parsed definition, so a key the parser
// dropped would be deleted by the rewrite and the following publish would succeed
// without the clause. Apply refuses instead, and the file keeps the user's text.
func TestFileBuilderApplyRefusesUnknownTaskKey(t *testing.T) {
	sources := &globalsource.Store{HomeDir: t.TempDir()}
	_, _, err := sources.CreateGraphDefinition("rna-seq", "RNA-seq")
	require.NoError(t, err)
	path := filepath.Join(sources.GraphsDir(), "rna-seq", "graph.yaml")
	const draft = `schemaVersion: 1
id: rna-seq
name: RNA-seq
budget:
  max_total_tokens: 1000
tasks:
  produce:
    hero: reviewer@1
    goal: Produce the QC table.
    acceptence:
      - file: outputs/qc.csv
        check: non-empty
`
	require.NoError(t, os.WriteFile(path, []byte(draft), 0o644))
	service := &Service{
		Sources: sources,
		Completer: CompleteFunc(func(context.Context, string, string, string) (string, error) {
			return `{"message":"Describe it","operations":[{"kind":"update_graph","description":"described"}]}`, nil
		}),
	}
	thread, err := service.Send(context.Background(), "rna-seq", "p7/m1", "Describe it")
	require.NoError(t, err)
	require.NotNil(t, thread.Proposal)

	_, _, err = service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.Error(t, err)
	var unknown *UnknownTaskKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []string{"acceptence"}, unknown.Fields["produce"])
	assert.Contains(t, err.Error(), "graph.yaml directly")

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, draft, string(onDisk), "a refused apply must not rewrite the draft")
}

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
	_, _, err := sources.CreateGraph("rna-seq", "RNA-seq")
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

	document, _, err := service.Apply(context.Background(), "rna-seq", thread.Proposal.ID)
	require.NoError(t, err)
	assert.Contains(t, document.Tasks, "align")
	reloaded, err := service.GetThread(context.Background(), "rna-seq")
	require.NoError(t, err)
	assert.Nil(t, reloaded.Proposal)

	info, err := os.Stat(filepath.Join(builderDir, "messages.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

package tools

import (
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

func NewDefaultRegistry(manager *workspace.Manager, artifactSinks ...*ArtifactSink) (*Registry, error) {
	var artifactSink *ArtifactSink
	if len(artifactSinks) > 0 {
		artifactSink = artifactSinks[0]
	}
	registered := []Tool{
		&ReadTool{Jail: manager.Jail},
		&WriteTool{Jail: manager.Jail},
		&EditTool{Jail: manager.Jail},
		&ListTool{Jail: manager.Jail},
		&GrepTool{Jail: manager.Jail},
		&FindTool{Jail: manager.Jail},
		&ExecTool{Workspace: manager, Artifacts: artifactSink},
		&BashTool{Workspace: manager, Artifacts: artifactSink},
		&WebFetchTool{},
	}
	if artifactSink != nil {
		registered = append(registered, &PublishArtifactTool{Jail: manager.Jail, Sink: artifactSink})
	}
	return NewRegistry(registered...)
}

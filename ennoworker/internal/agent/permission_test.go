package agent

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestClassifyToolRisk(t *testing.T) {
	tests := map[string]domain.RiskClass{
		"read": domain.RiskReadOnly, "ls": domain.RiskReadOnly, "search_compacted_history": domain.RiskReadOnly, "todo": domain.RiskReadOnly,
		"write": domain.RiskLocalWrite, "edit": domain.RiskLocalWrite,
		"exec": domain.RiskShell, "bash": domain.RiskShell,
		"web_fetch":   domain.RiskExternal,
		"credential":  domain.RiskSensitive,
		"future_tool": domain.RiskSensitive,
	}
	for tool, expected := range tests {
		assert.Equal(t, expected, ClassifyToolRisk(tool), tool)
	}
}

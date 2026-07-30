package agent

import "github.com/seqyuan/ennote/ennoworker/internal/domain"

func ClassifyToolRisk(toolName string) domain.RiskClass {
	switch toolName {
	case "read", "ls", "grep", "find", "search_compacted_history":
		return domain.RiskReadOnly
	case "write", "edit", "publish_artifact":
		return domain.RiskLocalWrite
	case "exec", "bash":
		return domain.RiskShell
	case "http", "web_fetch", "send_message", "mcp_call":
		return domain.RiskExternal
	case "process", "credential", "secret":
		return domain.RiskSensitive
	default:
		return domain.RiskSensitive
	}
}

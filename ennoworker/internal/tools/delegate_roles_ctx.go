package tools

type delegateRolesCtxKey string

const (
	delegateRolesRunIDKey     delegateRolesCtxKey = "delegate_run_id"
	delegateRolesSessionIDKey delegateRolesCtxKey = "delegate_session_id"
	delegateExecutionModeKey  delegateRolesCtxKey = "delegate_execution_mode"
	delegateAutoResumeKey     delegateRolesCtxKey = "delegate_auto_resume"
)

// DelegateExecutionModeKey carries the frozen execution mode to the provider.
var DelegateExecutionModeKey = delegateExecutionModeKey

// DelegateAutoResumeKey carries the frozen auto-resume flag to the provider.
var DelegateAutoResumeKey = delegateAutoResumeKey

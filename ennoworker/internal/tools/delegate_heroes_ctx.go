package tools

type delegateHeroesCtxKey string

const (
	delegateHeroesRunIDKey     delegateHeroesCtxKey = "delegate_run_id"
	delegateHeroesSessionIDKey delegateHeroesCtxKey = "delegate_session_id"
	delegateExecutionModeKey   delegateHeroesCtxKey = "delegate_execution_mode"
	delegateAutoResumeKey      delegateHeroesCtxKey = "delegate_auto_resume"
)

// DelegateExecutionModeKey carries the frozen execution mode to the provider.
var DelegateExecutionModeKey = delegateExecutionModeKey

// DelegateAutoResumeKey carries the frozen auto-resume flag to the provider.
var DelegateAutoResumeKey = delegateAutoResumeKey

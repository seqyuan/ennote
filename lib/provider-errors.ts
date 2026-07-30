const messages: Record<string, string> = {
  provider_configuration_invalid: "The provider runtime configuration is invalid.",
  provider_credential_unavailable: "The configured credential could not be resolved.",
  provider_authentication_failed: "The provider rejected the configured credential.",
  provider_model_not_found: "The configured model was not found by the provider.",
  provider_rate_limited: "The provider rate limit was reached. Try again later.",
  provider_request_timeout: "The provider did not respond before the timeout.",
  provider_request_rejected: "The provider rejected the model request.",
  provider_unavailable: "The provider is currently unavailable.",
  model_protocol_error: "The provider returned an invalid response.",
  model_stream_interrupted: "The provider response was interrupted.",
  context_overflow: "The request exceeds the model context window.",
  context_overflow_after_compaction: "The request still exceeds the context window after compaction.",
  context_overflow_in_run: "The active run exceeded the model context window.",
};

export function runFailureMessage(errorCode: unknown): string {
  const code = typeof errorCode === "string" ? errorCode : "";
  return messages[code] ?? (code ? `The run failed (${code}).` : "The run failed.");
}

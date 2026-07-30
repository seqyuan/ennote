"use client";

import { useEffect, useRef, useState } from "react";
import type { ModelProfile, ProviderDiagnostic, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export function ProviderSettingsRow({ provider, models }: { provider: ProviderProfile; models: ModelProfile[] }) {
  const [diagnostic, setDiagnostic] = useState<ProviderDiagnostic | null>(null);
  const [checking, setChecking] = useState(false);
  const [modelProfileId, setModelProfileId] = useState(models[0]?.id ?? "");
  const [testError, setTestError] = useState<string | null>(null);
  const requestVersion = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const selectedModelProfileId = models.some(model => model.id === modelProfileId) ? modelProfileId : (models[0]?.id ?? "");

  useEffect(() => () => controller.current?.abort(), []);

  async function testProvider() {
    controller.current?.abort();
    const activeController = new AbortController();
    controller.current = activeController;
    const version = ++requestVersion.current;
    setChecking(true);
    setTestError(null);
    try {
      const result = await apiFetch<ProviderDiagnostic>(`/v1/provider-profiles/${encodeURIComponent(provider.id)}/test`, {
        method: "POST",
        body: JSON.stringify(selectedModelProfileId ? { modelProfileId: selectedModelProfileId } : {}),
        signal: activeController.signal,
      });
      if (!activeController.signal.aborted && requestVersion.current === version) setDiagnostic(result);
    } catch (reason) {
      if (!activeController.signal.aborted && requestVersion.current === version) {
        setTestError((reason as Error).message);
        setDiagnostic(null);
      }
    } finally {
      if (!activeController.signal.aborted && requestVersion.current === version) setChecking(false);
    }
  }

  return <div className="settings-row provider-settings-row">
    <div className="settings-primary"><strong>{provider.name}</strong>
      <span>{provider.baseUrl} · {models.length} {models.length === 1 ? "model" : "models"}</span></div>
    <div className="provider-controls"><code>{provider.credentialRef}</code>
      <select aria-label={`Test model for ${provider.name}`} value={selectedModelProfileId} disabled={models.length === 0}
        onChange={event => setModelProfileId(event.target.value)}>
        {models.length === 0 ? <option value="">No model</option> : models.map(model =>
          <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
      </select>
      <button type="button" className="secondary-btn" onClick={testProvider}>{checking ? "Retest" : "Test"}</button>
    </div>
    {(diagnostic || testError) && <div className="provider-diagnostic" data-testid={`provider-diagnostic-${provider.id}`}>
      <div className={`diagnostic-summary diagnostic-${diagnostic?.status ?? "failed"}`}>
        <strong>{diagnostic?.status === "ready" ? "Ready" : "Failed"}</strong>
        <span>{diagnostic ? `${diagnostic.modelName ?? "No model"} · ${diagnostic.latencyMs} ms` : testError}</span>
      </div>
      {diagnostic?.failure && <span className="diagnostic-failure">{diagnostic.failure.message}
        {diagnostic.failure.requestId ? ` · ${diagnostic.failure.requestId}` : ""}</span>}
      {diagnostic && <div className="diagnostic-stages">{diagnostic.stages.map(stage =>
        <span key={stage.name} className={`diagnostic-stage diagnostic-stage-${stage.status}`}>
          {stage.name} · {stage.status}
        </span>)}</div>}
    </div>}
  </div>;
}

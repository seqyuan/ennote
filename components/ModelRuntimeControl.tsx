"use client";

import type { ModelProfile } from "@/components/settings/types";
import type { components } from "@/lib/worker-api.gen";

type ThinkingEffort = components["schemas"]["ThinkingEffort"];

export function ModelRuntimeControl({ models, modelProfileId, thinkingEffort, onModelChange, onEffortChange,
  disabledReason }: {
  models: ModelProfile[];
  modelProfileId: string;
  thinkingEffort: ThinkingEffort;
  onModelChange: (modelProfileId: string) => void;
  onEffortChange: (effort: ThinkingEffort) => void;
  disabledReason?: string;
}) {
  const selected = models.find((model) => model.id === modelProfileId);
  const efforts = selected?.supportedThinkingEfforts?.length
    ? selected.supportedThinkingEfforts
    : (["default"] as ThinkingEffort[]);

  return <div className="model-runtime-control" title={disabledReason}>
    <label>Model
      <select value={modelProfileId} disabled={Boolean(disabledReason)}
        onChange={(event) => onModelChange(event.target.value)}>
        <option value="" disabled>Select a model</option>
        {models.map((model) => <option key={model.id} value={model.id}>
          {model.displayName || model.modelName}
        </option>)}
      </select>
    </label>
    <label>Thinking effort
      <select value={efforts.includes(thinkingEffort) ? thinkingEffort : "default"}
        disabled={Boolean(disabledReason)}
        onChange={(event) => onEffortChange(event.target.value as ThinkingEffort)}>
        {efforts.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
      </select>
    </label>
  </div>;
}

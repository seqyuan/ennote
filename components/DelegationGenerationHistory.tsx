"use client";

import { History } from "lucide-react";
import type { components } from "@/lib/worker-api.gen";

type DelegationInspection = components["schemas"]["DelegationInspection"];

function formatTime(value: string | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// DelegationGenerationHistory is a compact disclosure over the explicit
// generation history of one delegation group: generation number, kind, status,
// selection/reuse counts, and timestamps. It is read-only and derived from the
// inspection projection.
export function DelegationGenerationHistory({ inspection }: { inspection: DelegationInspection }) {
  const generations = inspection.generations ?? [];
  if (generations.length === 0) return null;
  const current = inspection.currentGeneration ?? 0;
  return <details className="generation-history">
    <summary>
      <History size={12} aria-hidden="true" />
      <span>Generations ({generations.length})</span>
    </summary>
    <div className="generation-history-body">
      {generations.map(generation => (
        <div className="generation-row" key={generation.id} data-generation={generation.generation}>
          <strong>
            {generation.generation === current ? `Generation ${generation.generation} (current)` : `Generation ${generation.generation}`}
          </strong>
          <span>
            {generation.kind}
            {" · "}
            {generation.retrySelection?.length ?? 0} selected
            {" · "}
            {(generation.reusedAttempts?.length ?? 0)} reused
          </span>
          <span className="generation-status-tag" data-status={generation.status}>{generation.status}</span>
          <details className="generation-detail">
            <summary>Details</summary>
            <ul>
              <li>Created: <time>{formatTime(generation.createdAt)}</time></li>
              {generation.completedAt && <li>Completed: <time>{formatTime(generation.completedAt)}</time></li>}
              {generation.retrySelection?.length > 0 && (
                <li>Retried items: {generation.retrySelection.join(", ")}</li>
              )}
              {generation.reusedAttempts?.map(reference => (
                <li key={reference.attemptId}>
                  Reused {reference.itemId} · attempt {reference.generation}
                  {reference.resultDigest ? ` · ${reference.resultDigest.slice(0, 12)}…` : ""}
                </li>
              ))}
            </ul>
          </details>
        </div>
      ))}
    </div>
  </details>;
}

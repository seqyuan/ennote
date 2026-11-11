import type { components } from "@/lib/worker-api.gen";

type RunAgentFlow = components["schemas"]["RunAgentFlow"];

/**
 * Which run the Graphs channel should expand after a refresh.
 *
 * This is a pure decision rather than an inline `setExpanded` updater because it has
 * one non-obvious rule that used to be wrong: a reader who collapses the newest run
 * must keep it collapsed. The panel polls while a run is active, so "expand the
 * newest run when the list arrives" re-opened it seconds after the click, which made
 * the collapse button look broken — and, because the parent passed a new callback
 * identity on every render, the refresh could land immediately after the click.
 *
 * `dismissed` is the run the reader explicitly collapsed. A *newer* run is a new fact
 * and may expand again; the run they closed may not.
 */
export function nextExpandedRun(
  current: string | null,
  runs: readonly RunAgentFlow[],
  dismissed: string | null,
): string | null {
  if (runs.length === 0) return null;
  const newest = runs[0].runId ?? null;
  if (newest === null) return null;
  if (current === null) return newest === dismissed ? null : newest;
  if (!runs.some(run => run.runId === current)) return newest === dismissed ? null : newest;
  const newestCreated = runs[0].createdAt;
  const currentCreated = runs.find(run => run.runId === current)?.createdAt;
  if (newestCreated && currentCreated && currentCreated < newestCreated) {
    return newest === dismissed ? null : newest;
  }
  return current;
}

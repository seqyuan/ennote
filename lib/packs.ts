"use client";

/**
 * Agent Pack calls.
 *
 * Four loopback-only Worker endpoints: list what is installed, inspect a pack
 * read-only, plan or apply an install, and uninstall. A pack is a distribution
 * unit, not a runtime: installing one creates disabled draft objects, and the
 * installer never publishes a revision. Nothing here decides any of that — the
 * Worker does, and these wrappers only carry its answer back.
 */
import { apiFetch } from "./worker-api.client";
import type { components } from "./worker-api.gen";

export type PackStateSummary = components["schemas"]["PackStateSummary"];
export type PackInspectResult = components["schemas"]["PackInspectResult"];
export type PackInstallPlan = components["schemas"]["PackInstallPlan"];
export type PackIssue = components["schemas"]["PackIssue"];
export type PackAction = components["schemas"]["PackAction"];

/** A pack source. Only host paths are supported; the Worker confines them. */
export interface PackPathSource {
  kind: "path";
  path: string;
}

/** Installed packs, ordered by pack id. */
export function listPacks(): Promise<PackStateSummary[]> {
  return apiFetch<PackStateSummary[]>("/v1/packs");
}

/**
 * Read a pack without installing it.
 * @param path - absolute host path inside the Worker host home.
 */
export function inspectPack(path: string): Promise<PackInspectResult> {
  return apiFetch<PackInspectResult>("/v1/packs/inspect", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source: { kind: "path", path } satisfies PackPathSource }),
  });
}

/**
 * Plan an install, or apply it.
 *
 * A dry run always answers with the plan, blockers included, and writes
 * nothing. A real install answers 422 while any blocker or conflict remains, so
 * the panel shows the dry run first and only then offers to apply it.
 */
export function installPack(path: string, options: { dryRun: boolean; expectedDigest?: string }): Promise<PackInstallPlan> {
  return apiFetch<PackInstallPlan>("/v1/packs/install", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      source: { kind: "path", path } satisfies PackPathSource,
      dryRun: options.dryRun,
      ...(options.expectedDigest ? { expectedDigest: options.expectedDigest } : {}),
    }),
  });
}

/**
 * Uninstall a pack.
 *
 * The Worker answers 204 with no body, so this resolves to nothing: a failure
 * throws like every other call, which is the only outcome worth branching on.
 * @param force - delete objects that were edited locally, or that another Graph
 *   still references. A locally edited Skill is always kept, with a warning.
 */
export function removePack(publisher: string, name: string, force = false): Promise<void> {
  const query = force ? "?force=true" : "";
  return apiFetch<void>(
    `/v1/packs/${encodeURIComponent(publisher)}/${encodeURIComponent(name)}${query}`,
    { method: "DELETE" },
  );
}

/**
 * Split a pack id into the two path segments the endpoints take.
 * @returns the segments, or `null` when the id is not `publisher/name`.
 */
export function packSlug(packId: string): { publisher: string; name: string } | null {
  const parts = packId.split("/");
  if (parts.length !== 2 || parts[0] === "" || parts[1] === "") return null;
  return { publisher: parts[0], name: parts[1] };
}

/**
 * The issues worth showing, blockers first.
 *
 * `severity` is optional in the contract, so it is filled here rather than at
 * the render: a blocker defaults to an error and a warning to a warning, which
 * is what the two arrays mean.
 */
export function packIssues(plan: Pick<PackInstallPlan, "blockers" | "warnings">): PackIssue[] {
  return [
    ...plan.blockers.map((issue) => ({ ...issue, severity: issue.severity ?? "error" })),
    ...plan.warnings.map((issue) => ({ ...issue, severity: issue.severity ?? "warning" })),
  ];
}

/**
 * Right-panel file tabs are cache rows scoped by Project: a row opened in one
 * Project must never render while another Project is selected (AGENTS.md scopes
 * every cache row by its owner). The rows are derived from the selected
 * projectId on every render rather than pruned in an effect, so no stale list
 * and no late update from the previous Project can reach the panel — there is
 * no generation window to track.
 */
import { PANEL_CHANNELS } from "./right-panel-channels";

export function scopeFileTabs<T extends { projectId?: string }>(tabs: T[], projectId: string | null): T[] {
  if (!projectId) return [];
  return tabs.filter((tab) => tab.projectId === projectId);
}

/** Panel tabs that are not file rows; they belong to the workspace, not a Project. */
const PANEL_TAB_IDS: ReadonlySet<string> = new Set(PANEL_CHANNELS);

/**
 * The active tab after scoping. A file row that the current Project does not
 * own falls back to the Files panel; the workspace panels stay selected.
 */
export function resolveActiveTabId(activeTabId: string, scopedFileTabIds: readonly string[]): string {
  if (PANEL_TAB_IDS.has(activeTabId)) return activeTabId;
  return scopedFileTabIds.includes(activeTabId) ? activeTabId : "files";
}

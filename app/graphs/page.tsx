import { AppShell } from "@/components/AppShell";

/**
 * `/graphs` — direct-entry / legacy URL for the Graphs view. Renders the
 * same AppShell; the sidebar switches views in-place, so this route is
 * only reached by bookmarks, deep links, and tests. The current view is
 * synced to `/?view=graphs` via history.replaceState.
 */
export default function GraphsPage() {
  return <AppShell initialView="graphs" />;
}

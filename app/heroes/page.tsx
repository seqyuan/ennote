import { AppShell } from "@/components/AppShell";

/**
 * `/heroes` — direct-entry / legacy URL for the Heroes view. Renders the
 * same AppShell; the sidebar switches views in-place, so this route is
 * only reached by bookmarks, deep links, and tests. The current view is
 * synced to `/?view=heroes` via history.replaceState.
 */
export default function HeroesPage() {
  return <AppShell initialView="heroes" />;
}

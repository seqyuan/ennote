import { AppShell } from "@/components/AppShell";

/**
 * `/roles` — direct-entry / legacy URL for the Roles view. Renders the
 * same AppShell; the sidebar switches views in-place, so this route is
 * only reached by bookmarks, deep links, and tests. The current view is
 * synced to `/?view=roles` via history.replaceState.
 */
export default function RolesPage() {
  return <AppShell initialView="roles" />;
}

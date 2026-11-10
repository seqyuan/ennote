import type { Tab } from "../TabBar";

/**
 * The three channels that live in the left lane, in strip order.
 *
 * They are not file rows: they belong to the workspace, so no `projectId` — and
 * no `closable` decision — is attached here. Whether a channel can be closed is
 * the panel's business, not the strip's.
 */
export const CHANNEL_TABS: readonly Tab[] = [
  { id: "files", label: "Files", closable: false, icon: "files" },
  { id: "graph", label: "Graphs", closable: false, icon: "graph" },
  { id: "tools", label: "Status", closable: false, icon: "tools" },
];

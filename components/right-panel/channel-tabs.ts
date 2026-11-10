import type { Tab } from "../TabBar";
import type { PanelChannel } from "@/lib/right-panel-channels";
import type { Translator } from "@/lib/messages";

/**
 * The three channels that live in the left lane, in strip order.
 *
 * They are not file rows: they belong to the workspace, so no `projectId` is
 * attached. Each carries its own close button — closing a channel narrows the
 * strip without touching the pin that restores it later.
 */
export const CHANNEL_ORDER: readonly Exclude<PanelChannel, never>[] = ["files", "graph", "tools"];

const LABEL_KEYS: Record<PanelChannel, string> = {
  files: "rightPanel.files",
  graph: "rightPanel.graphs",
  tools: "rightPanel.status",
};

const ICONS: Record<PanelChannel, Tab["icon"]> = {
  files: "files",
  graph: "graph",
  tools: "tools",
};

/** One channel's strip label in the reader's language. */
export function channelLabel(channel: PanelChannel, t: Translator): string {
  return t(LABEL_KEYS[channel]);
}

/** The channel rows for the strip, labelled for the active locale. */
export function channelTabs(channels: readonly PanelChannel[], t: Translator): Tab[] {
  return channels.map((channel) => ({
    id: channel,
    label: channelLabel(channel, t),
    closable: true,
    icon: ICONS[channel],
  }));
}

import type { Tab } from "../TabBar";
import type { PanelChannel } from "@/lib/right-panel-channels";
import type { Translator } from "@/lib/messages";

/**
 * The channels that live in the left lane, in strip order.
 *
 * They are not file rows: they belong to the workspace, so no `projectId` is
 * attached. Each carries its own close button — closing a channel narrows the
 * strip without touching the pin that restores it later.
 */
const LABEL_KEYS: Record<PanelChannel, string> = {
  files: "rightPanel.files",
  graph: "rightPanel.graphs",
  tools: "rightPanel.status",
  canvas: "rightPanel.canvas",
};

const ICONS: Record<PanelChannel, Tab["icon"]> = {
  files: "files",
  graph: "graph",
  tools: "tools",
  canvas: "canvas",
};

/** One channel's strip label in the reader's language. */
export function channelLabel(channel: PanelChannel, t: Translator): string {
  return t(LABEL_KEYS[channel]);
}

/** What a channel has to say about itself, drawn on its tab. */
export interface ChannelSignal {
  /** A count, e.g. the runs this session has. */
  count?: number;
  /** Something is waiting for a human. */
  attention?: boolean;
}

/** The channel rows for the strip, labelled for the active locale. */
export function channelTabs(
  channels: readonly PanelChannel[],
  t: Translator,
  signal: Partial<Record<PanelChannel, ChannelSignal>> = {},
): Tab[] {
  return channels.map((channel) => ({
    id: channel,
    label: channelLabel(channel, t),
    closable: true,
    icon: ICONS[channel],
    badge: signal[channel]?.count,
    dot: signal[channel]?.attention,
    signalLabel: signal[channel]?.count !== undefined
      ? t("rightPanel.runsCount", { count: signal[channel]?.count ?? 0 })
      : signal[channel]?.attention
        ? t("rightPanel.needsAttention")
        : undefined,
  }));
}

"use client";

import { Files, LayoutGrid, Workflow, Wrench } from "lucide-react";

import type { PanelChannel } from "@/lib/right-panel-channels";
import type { Translator } from "@/lib/messages";
import { channelLabel } from "./channel-tabs";

const ICONS: Record<PanelChannel, typeof Files> = {
  files: Files,
  graph: Workflow,
  tools: Wrench,
  canvas: LayoutGrid,
};

/**
 * What the channel lane shows when the reader has closed every channel.
 *
 * It lists every channel rather than a hint, because "you closed everything" is a
 * dead end otherwise: the strip has nothing left to click.
 */
export function ChannelLauncher(props: {
  channels: readonly PanelChannel[];
  hint: string;
  t: Translator;
  onOpen: (channel: PanelChannel) => void;
}) {
  return (
    <div className="right-panel-launcher" role="group" aria-label={props.hint}>
      <p className="right-panel-launcher-hint">{props.hint}</p>
      {props.channels.map((channel) => {
        const Icon = ICONS[channel];
        return (
          <button
            key={channel}
            type="button"
            className="right-panel-launcher-card"
            onClick={() => props.onOpen(channel)}
          >
            <Icon size={15} aria-hidden="true" />
            <span>{channelLabel(channel, props.t)}</span>
          </button>
        );
      })}
    </div>
  );
}

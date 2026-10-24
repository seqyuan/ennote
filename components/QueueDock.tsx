"use client";

import { ChevronDown, ChevronUp, CornerUpRight, ListPlus, Pencil, X } from "lucide-react";
import { useState } from "react";
import { useT } from "@/components/LocaleProvider";

export interface QueuedMessage {
  id: string;
  text: string;
}

/**
 * Durable queued follow-ups, shown above the composer. Edit/remove cancel the
 * queued row on the Worker first; "Steer now" cancels and re-sends the text as
 * an immediate steer.
 */
export function QueueDock({
  items,
  onEdit,
  onRemove,
  onSteer,
}: {
  items: QueuedMessage[];
  onEdit: (item: QueuedMessage) => void;
  onRemove: (item: QueuedMessage) => void;
  onSteer: (item: QueuedMessage) => void;
}) {
  const t = useT();
  const [collapsed, setCollapsed] = useState(false);
  if (items.length === 0) return null;
  const expanded = !collapsed || items.length === 1;

  return (
    <section className="queue-dock" aria-label={t("composer.queue.aria")}>
      <button
        type="button"
        className="queue-dock-header"
        aria-expanded={expanded}
        onClick={() => setCollapsed((value) => !value)}
      >
        <ListPlus size={14} aria-hidden="true" />
        <span className="queue-dock-title">{t("composer.queue.title")}</span>
        <span className="queue-dock-count">{items.length}</span>
        <span className="queue-dock-summary">
          {t("composer.queue.count").replace("{count}", String(items.length))}
        </span>
        <span className="queue-dock-chevron" aria-hidden="true">
          {expanded ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
        </span>
      </button>
      {expanded && (
        <ul className="queue-dock-list">
          {items.map((item, index) => (
            <li className="queue-dock-row" key={item.id}>
              <span className="queue-dock-index" aria-hidden="true">{index + 1}</span>
              <span className="queue-dock-text" title={item.text}>{item.text}</span>
              <span className="queue-dock-actions">
                <button
                  type="button"
                  className="queue-dock-action"
                  title={t("composer.queue.edit")}
                  aria-label={t("composer.queue.edit")}
                  onClick={() => onEdit(item)}
                >
                  <Pencil size={13} aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="queue-dock-action"
                  title={t("composer.queue.steer")}
                  aria-label={t("composer.queue.steer")}
                  onClick={() => onSteer(item)}
                >
                  <CornerUpRight size={13} aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="queue-dock-action is-danger"
                  title={t("composer.queue.remove")}
                  aria-label={t("composer.queue.remove")}
                  onClick={() => onRemove(item)}
                >
                  <X size={13} aria-hidden="true" />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

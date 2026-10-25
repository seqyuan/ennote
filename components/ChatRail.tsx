"use client";

import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { useT } from "@/components/LocaleProvider";
import type { ConversationNode } from "@/lib/chat-messages";
import styles from "./ChatRail.module.css";

interface RailTurn {
  id: string;
  index: number;
  text: string;
}

function compactText(value: string | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim();
}

/**
 * Turn rail beside the transcript: one marker per user turn, active marker
 * follows reading position, hover/focus previews the prompt, click jumps.
 * Borrowed from pi-web's ChatMinimap and dsh's turn navigator, kept to the
 * parts that are useful without a virtualized transcript.
 */
export function ChatRail({
  nodes,
  scrollContainer,
}: {
  nodes: ConversationNode[];
  scrollContainer: RefObject<HTMLDivElement | null>;
}) {
  const t = useT();
  const turns = useMemo<RailTurn[]>(() => nodes
    .filter((node) => node.kind === "turn")
    .map((turn, index) => ({
      id: turn.id,
      index,
      text: compactText(turn.user?.text),
    })), [nodes]);
  const [activeIndex, setActiveIndex] = useState(0);
  const [hovered, setHovered] = useState<number | null>(null);
  const frame = useRef<number | null>(null);

  const turnElement = (container: HTMLElement, turn: RailTurn): HTMLElement | null => (
    container.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(turn.id)}"]`)
  );

  const scrollToTurn = (turn: RailTurn) => {
    const container = scrollContainer.current;
    if (!container) return;
    const element = turnElement(container, turn);
    if (!element) return;
    const top = element.getBoundingClientRect().top
      - container.getBoundingClientRect().top
      + container.scrollTop
      - 12;
    container.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
    setActiveIndex(turn.index);
  };

  useEffect(() => {
    const container = scrollContainer.current;
    if (!container) return;
    const measureActive = () => {
      if (turns.length === 0) return;
      const focus = container.scrollTop + container.clientHeight * 0.3;
      let best = 0;
      let bestDistance = Number.POSITIVE_INFINITY;
      const containerTop = container.getBoundingClientRect().top;
      for (const turn of turns) {
        const element = turnElement(container, turn);
        if (!element) continue;
        const top = element.getBoundingClientRect().top - containerTop + container.scrollTop;
        const distance = Math.abs(top - focus);
        if (distance < bestDistance) {
          bestDistance = distance;
          best = turn.index;
        }
      }
      setActiveIndex(best);
    };
    const schedule = () => {
      if (frame.current !== null) return;
      frame.current = window.requestAnimationFrame(() => {
        frame.current = null;
        measureActive();
      });
    };
    container.addEventListener("scroll", schedule, { passive: true });
    const observer = new ResizeObserver(schedule);
    observer.observe(container);
    schedule();
    return () => {
      container.removeEventListener("scroll", schedule);
      observer.disconnect();
      if (frame.current !== null) window.cancelAnimationFrame(frame.current);
      frame.current = null;
    };
  }, [scrollContainer, turns]);

  if (turns.length < 2) return null;

  const labelFor = (turn: RailTurn) => turn.text || t("conversation.rail.turn").replace("{index}", String(turn.index + 1));

  return (
    <nav className={styles.rail} aria-label={t("conversation.rail.aria")}>
      <span className={styles.track} aria-hidden="true" />
      {turns.map((turn) => {
        const top = ((turn.index + 0.5) / turns.length) * 100;
        const label = labelFor(turn);
        const active = activeIndex === turn.index;
        return (
          <button
            key={turn.id}
            type="button"
            className={styles.marker}
            style={{ top: `${top}%` }}
            data-active={active || undefined}
            aria-current={active ? "true" : undefined}
            aria-label={t("conversation.rail.goTo").replace("{index}", String(turn.index + 1)).replace("{label}", label)}
            onClick={() => scrollToTurn(turn)}
            onMouseEnter={() => setHovered(turn.index)}
            onMouseLeave={() => setHovered((current) => current === turn.index ? null : current)}
            onFocus={() => setHovered(turn.index)}
            onBlur={() => setHovered((current) => current === turn.index ? null : current)}
          >
            <span className={styles.dot} aria-hidden="true" />
          </button>
        );
      })}
      {hovered !== null && turns[hovered] && (
        <div
          className={styles.preview}
          style={{ top: `${((hovered + 0.5) / turns.length) * 100}%` }}
          role="tooltip"
        >
          <span className={styles.previewIndex}>{String(hovered + 1).padStart(2, "0")}</span>
          <span className={styles.previewText}>{labelFor(turns[hovered])}</span>
        </div>
      )}
    </nav>
  );
}

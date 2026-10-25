"use client";

import { Check, Copy, MessageSquareQuote, Sparkles } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";
import type { TextSelection } from "@/hooks/useTextSelection";
import styles from "./SelectionActions.module.css";

interface Props {
  selection: TextSelection;
  onQuote: (text: string) => void;
  onExplain: (text: string) => void;
  onDismiss: () => void;
}

export function SelectionActions({ selection, onQuote, onExplain, onDismiss }: Props) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | null>(null);

  useOverlayLayer(true, onDismiss);

  const copy = useCallback(() => {
    void navigator.clipboard?.writeText(selection.text).then(() => {
      setCopied(true);
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), 1000);
    }).catch(() => { /* clipboard unavailable outside a secure context */ });
  }, [selection.text]);

  const above = selection.rect.top > 56;
  const center = typeof window === "undefined"
    ? selection.rect.left
    : Math.min(window.innerWidth - 150, Math.max(150, selection.rect.left + selection.rect.width / 2));

  return (
    <div
      className={styles.toolbar}
      data-placement={above ? "above" : "below"}
      role="toolbar"
      aria-label={t("selection.aria")}
      style={{ left: center, top: above ? selection.rect.top - 8 : selection.rect.bottom + 8 }}
      onPointerDown={(event) => event.preventDefault()}
    >
      <button type="button" className={styles.action} onClick={() => onQuote(selection.text)}>
        <MessageSquareQuote size={14} aria-hidden="true" />
        {t("selection.quote")}
      </button>
      <button type="button" className={styles.action} onClick={copy}>
        {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
        {t(copied ? "selection.copied" : "selection.copy")}
      </button>
      <button type="button" className={styles.action} onClick={() => onExplain(selection.text)}>
        <Sparkles size={14} aria-hidden="true" />
        {t("selection.explain")}
      </button>
    </div>
  );
}

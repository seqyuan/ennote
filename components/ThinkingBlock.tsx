"use client";

import { Brain } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export function ThinkingBlock({ text }: { text: string }) {
  const t = useT();
  return <details className="thinking-block">
    <summary><Brain size={14} aria-hidden="true" /><span>{t("conversation.thinking.reasoning")}</span></summary>
    <div className="thinking-content">{text}</div>
  </details>;
}

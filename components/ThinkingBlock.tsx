"use client";

import { Brain } from "lucide-react";

export function ThinkingBlock({ text }: { text: string }) {
  return <details className="thinking-block">
    <summary><Brain size={14} aria-hidden="true" /><span>Reasoning</span></summary>
    <div className="thinking-content">{text}</div>
  </details>;
}

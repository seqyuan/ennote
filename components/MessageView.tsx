"use client";

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export function MessageView({ text, className = "" }: { text: string; className?: string }) {
  return <div className={`markdown-body ${className}`.trim()}>
    <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
  </div>;
}

"use client";

import { Bot, ImageIcon } from "lucide-react";
import { MessageView } from "@/components/MessageView";
import { ThinkingBlock } from "@/components/ThinkingBlock";
import type { AssistantStep } from "@/lib/chat-messages";

export function AssistantMessage({ step }: { step: AssistantStep }) {
  const roleSpeaker = step.speaker?.kind === "role";
  const label = step.speaker?.handle ? `@${step.speaker.handle}` : (step.speaker?.displayName || "Host");
  return <section className="assistant-step" data-message-id={step.sourceMessageId}>
    <div className={roleSpeaker ? "assistant-speaker role" : "assistant-speaker"}>
      <span style={step.speaker?.color ? { color: step.speaker.color } : undefined}><Bot size={13} aria-hidden="true" /></span>
      <strong>{label}</strong>{roleSpeaker && <small>Role</small>}
    </div>
    {step.blocks.map((block, index) => {
      if (block.kind === "text") return <MessageView key={index} text={block.text} />;
      if (block.kind === "thinking") return <ThinkingBlock key={index} text={block.text} />;
      if (block.kind === "image") return <div className="image-reference" key={index}>
        <ImageIcon size={15} aria-hidden="true" /> Image {block.width}×{block.height}
      </div>;
      return <details className="image-description" key={index}>
        <summary>Image description</summary><div>{block.text}</div>
      </details>;
    })}
  </section>;
}

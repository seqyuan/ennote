import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ConversationTurn } from "@/components/ConversationTurn";
import type { AssistantStep, ConversationTurn as Turn } from "@/lib/chat-messages";

function assistant(id: string, text: string): AssistantStep {
  return { kind: "assistant", id, blocks: [{ kind: "text", text }], speaker: { kind: "host", displayName: "Host" } };
}

function turn(steps: AssistantStep[]): Turn {
  return { kind: "turn", id: "turn-1", user: { id: "u1", text: "question" }, steps, messageIds: ["u1"] };
}

describe("conversation turn speaker rendering", () => {
  it("renders one speaker row for a turn with several assistant steps", () => {
    const html = renderToStaticMarkup(
      <ConversationTurn sessionId="s1" turn={turn([assistant("a1", "first"), assistant("a2", "second"), assistant("a3", "final")])} />,
    );
    expect(html.match(/assistant-speaker/g) ?? []).toHaveLength(1);
    expect(html).toContain('data-variant="commentary"');
    expect(html).toContain('data-continuation="true"');
    expect(html).toContain("final");
  });

  it("keeps the speaker row for a single-step turn", () => {
    const html = renderToStaticMarkup(<ConversationTurn sessionId="s1" turn={turn([assistant("a1", "only")])} />);
    expect(html.match(/assistant-speaker/g) ?? []).toHaveLength(1);
    expect(html).not.toContain('data-variant="commentary"');
  });
});

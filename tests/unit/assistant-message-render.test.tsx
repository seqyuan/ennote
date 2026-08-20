import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AssistantMessage } from "@/components/AssistantMessage";
import type { AssistantStep } from "@/lib/chat-messages";

function hostStep(modelName?: string, displayName?: string): AssistantStep {
  return {
    kind: "assistant",
    id: "a-host",
    blocks: [{ kind: "text", text: "hello" }],
    speaker: { kind: "host", ...(displayName ? { displayName } : {}), ...(modelName ? { modelName } : {}) },
  };
}

function roleStep(partial: Partial<NonNullable<AssistantStep["speaker"]>>): AssistantStep {
  return { kind: "assistant", id: "a-role", blocks: [{ kind: "text", text: "reply" }], speaker: { kind: "role", ...partial } };
}

describe("AssistantMessage speaker attribution rendering", () => {
  it("host reply shows both the Host role label and the model name", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={hostStep("Claude Sonnet 4")} />);
    expect(html).toContain("Host");
    expect(html).toContain("Claude Sonnet 4");
    expect(html).toContain("assistant-model");
  });

  it("host reply shows only the Host label when no model is known", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={hostStep()} />);
    expect(html).toContain("Host");
    expect(html).not.toContain("assistant-model");
  });

  it("host reply prefers a custom displayName over the literal Host", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={hostStep("gpt-5", "My Agent")} />);
    expect(html).toContain("My Agent");
    expect(html).toContain("gpt-5");
  });

  it("role reply shows @handle, the Role badge and the model name", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={roleStep({ handle: "qc-analyst", modelName: "deepseek-v4" })} />);
    expect(html).toContain("@qc-analyst");
    expect(html).toContain("Role");
    expect(html).toContain("deepseek-v4");
  });

  it("role reply without a handle falls back to displayName without the @ prefix", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={roleStep({ displayName: "QC Analyst", modelName: "deepseek-v4" })} />);
    expect(html).toContain("QC Analyst");
    expect(html).not.toContain("@QC");
    expect(html).toContain("Role");
  });

  it("role reply with neither handle nor model shows only the fallback label and badge", () => {
    const html = renderToStaticMarkup(<AssistantMessage step={roleStep({ displayName: "QC Analyst" })} />);
    expect(html).toContain("QC Analyst");
    expect(html).toContain("Role");
    expect(html).not.toContain("assistant-model");
  });
});

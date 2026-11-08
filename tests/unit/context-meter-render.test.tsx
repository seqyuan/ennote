import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ContextMeter } from "@/components/ContextMeter";
import type { SessionContextUsage, SessionStats } from "@/hooks/chat-controller-types";

function usage(overrides: Partial<SessionContextUsage> = {}): SessionContextUsage {
  return {
    contextWindow: 100000,
    projectedTokens: 40000,
    systemTokens: 8000,
    toolsTokens: 6000,
    messageTokens: 26000,
    ...overrides,
  };
}

function stats(overrides: Partial<SessionStats> = {}): SessionStats {
  return {
    turns: 3, steps: 12, llmMs: 10900, toolMs: 88000,
    ttftSteps: 4, ttftMs: 1600, decodeMs: 20000, decodeTokens: 2920,
    uncachedInputTokens: 5500, cacheReadTokens: 27300, cacheWriteTokens: 0,
    outputTokens: 1300, reasoningTokens: 0,
    ...overrides,
  } as SessionStats;
}

describe("ContextMeter category rows", () => {
  it("renders the seven ennote categories when the Worker reports them", () => {
    const categories = [
      { key: "system", tokens: 4000 },
      { key: "hero", tokens: 3000 },
      { key: "rules", tokens: 2000 },
      { key: "skills", tokens: 1000 },
      { key: "tools", tokens: 6000 },
      { key: "reminders", tokens: 2000 },
      { key: "conversation", tokens: 22000 },
    ];
    const html = renderToStaticMarkup(<ContextMeter contextUsage={usage({ categories, categoriesVersion: 1 })} initialOpen />);
    for (const label of ["System prompt", "Hero", "Project rules", "Skills", "Tools", "Reminders", "Conversation"]) {
      expect(html).toContain(label);
    }
    expect(html).toContain("cm-color-hero");
    expect(html).toContain("cm-color-reminders");
  });

  it("marks the ring when occupancy reaches the model reminder and the near-full line", () => {
    const calm = renderToStaticMarkup(<ContextMeter contextUsage={usage({ projectedTokens: 40000 })} />);
    const high = renderToStaticMarkup(<ContextMeter contextUsage={usage({ projectedTokens: 85000 })} />);
    const critical = renderToStaticMarkup(<ContextMeter contextUsage={usage({ projectedTokens: 95000 })} />);
    expect(calm).not.toContain("is-high");
    expect(calm).not.toContain("is-critical");
    expect(high).toContain("is-high");
    expect(critical).toContain("is-critical");
  });

  it("falls back to the three legacy totals without categories", () => {
    const html = renderToStaticMarkup(<ContextMeter contextUsage={usage()} initialOpen />);
    expect(html).toContain("System prompt");
    expect(html).toContain("Tools");
    expect(html).toContain("Messages");
    expect(html).not.toContain("Project rules");
    expect(html).not.toContain("Reminders");
  });
});

describe("ContextMeter session figures", () => {
  // The composer no longer prints a stats strip: these numbers open from the ring.
  it("renders the session aggregates inside the breakdown panel", () => {
    const html = renderToStaticMarkup(<ContextMeter contextUsage={usage()} stats={stats()} initialOpen />);
    expect(html).toContain("Session figures");
    expect(html).toContain("3 turns · 12 steps");
    expect(html).toContain("Cache hit 83%");
    expect(html).toContain("Input 32.8K tok · Output 1.3K tok");
  });

  it("keeps the figures reachable when the Worker reports no context window", () => {
    const html = renderToStaticMarkup(<ContextMeter contextUsage={null} stats={stats()} initialOpen />);
    expect(html).toContain("Session figures");
    expect(html).toContain("3 turns · 12 steps");
    // No ring: a 0% circle would claim an empty budget the Worker never reported.
    expect(html).not.toContain("context-meter-fill");
  });

  it("renders nothing when there is neither a projection nor figures", () => {
    expect(renderToStaticMarkup(<ContextMeter contextUsage={null} stats={null} />)).toBe("");
    expect(renderToStaticMarkup(<ContextMeter contextUsage={null} stats={stats({ turns: 0, steps: 0, llmMs: 0, toolMs: 0, ttftSteps: 0, ttftMs: 0, decodeMs: 0, decodeTokens: 0, uncachedInputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, outputTokens: 0 })} />)).toBe("");
  });
});

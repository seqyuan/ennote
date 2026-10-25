import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ContextMeter } from "@/components/ContextMeter";
import type { SessionContextUsage } from "@/hooks/chat-controller-types";

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

describe("ContextMeter category rows", () => {
  it("renders the seven ennote categories when the Worker reports them", () => {
    const categories = [
      { key: "system", tokens: 4000 },
      { key: "role", tokens: 3000 },
      { key: "rules", tokens: 2000 },
      { key: "skills", tokens: 1000 },
      { key: "tools", tokens: 6000 },
      { key: "reminders", tokens: 2000 },
      { key: "conversation", tokens: 22000 },
    ];
    const html = renderToStaticMarkup(<ContextMeter contextUsage={usage({ categories, categoriesVersion: 1 })} initialOpen />);
    for (const label of ["System prompt", "Role", "Project rules", "Skills", "Tools", "Reminders", "Conversation"]) {
      expect(html).toContain(label);
    }
    expect(html).toContain("cm-color-role");
    expect(html).toContain("cm-color-reminders");
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

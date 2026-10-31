import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DelegationStrip, nextStripState } from "@/components/BackgroundDelegationStrip";
import type { BackgroundDelivery } from "@/hooks/useDelegationDelivery";
import { translate } from "@/lib/messages";
import type { components } from "@/lib/worker-api.gen";

const t = (key: string) => translate("en", key);

type DelegationHandle = components["schemas"]["DelegationHandle"];
type DelegationCompletion = components["schemas"]["DelegationCompletion"];

function handle(overrides: Partial<DelegationHandle> = {}): DelegationHandle {
  return {
    id: "handle-bg",
    groupId: "group-bg",
    sessionId: "session-1",
    sourceParentRunId: "parent-run",
    sourceBranchId: "branch",
    executionMode: "background",
    autoResume: false,
    status: "completed",
    createdAt: "2026-08-04T00:00:03Z",
    updatedAt: "2026-08-04T00:00:04Z",
    ...overrides,
  };
}

function completion(overrides: Partial<DelegationCompletion> = {}): DelegationCompletion {
  return {
    id: "completion-bg",
    handleId: "handle-bg",
    sessionId: "session-1",
    generation: 0,
    kind: "completed",
    result: {},
    resultDigest: "sha256:aa",
    sequence: 1,
    deliveryStatus: "consumed_by_parent",
    createdAt: "2026-08-04T00:00:04Z",
    ...overrides,
  };
}

function delivery(overrides: Partial<BackgroundDelivery> = {}): BackgroundDelivery {
  return { handle: handle(), completion: completion(), ...overrides };
}

describe("BackgroundDelegationStrip", () => {
  it("shows a durable completion by default, without a user expand", () => {
    const html = renderToStaticMarkup(<DelegationStrip deliveries={[delivery()]} refreshing={false} />);
    expect(html).toContain("background-delegation-strip");
    expect(html).toContain('aria-expanded="true"');
    expect(html).toContain("delivery-list");
    // Localized delivery status, not the raw enum with underscores.
    expect(html).toContain(t("delegation.handle.consumed_by_parent"));
    expect(html).toContain('<div class="delivery-row" data-delivery-status="consumed_by_parent"');
    expect(html).not.toContain(">consumed_by_parent<");
  });

  it("keeps active-only work collapsed", () => {
    const html = renderToStaticMarkup(
      <DelegationStrip
        deliveries={[delivery({ completion: undefined, handle: handle({ status: "active" }) })]}
        refreshing={false}
      />,
    );
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("delivery-list");
    expect(html).toContain(t("conversation.delegation.active").replace("{count}", "1"));
  });

  it("keeps the toggle outside the live region and announces the summary", () => {
    const html = renderToStaticMarkup(<DelegationStrip deliveries={[delivery()]} refreshing={false} />);
    // The strip itself is no longer a live region wrapping a button.
    expect(html).not.toContain('role="status" aria-label');
    expect(html.split('role="status"')).toHaveLength(2);
    const button = html.slice(html.indexOf("<button"), html.indexOf("</button>"));
    expect(button).toContain('aria-expanded="true"');
    expect(button).not.toContain('role="status"');
    expect(html).toContain(t("conversation.delegation.settled"));
  });

  it("opens on a new completion but respects an explicit collapse", () => {
    const collapsed = { seenCompletions: 1, collapsed: true };
    // Polling refresh returns the same completion: the user's collapse wins.
    expect(nextStripState(collapsed, 1)).toBe(collapsed);
    // A new durable completion is news: open once.
    expect(nextStripState(collapsed, 2)).toEqual({ seenCompletions: 2, collapsed: false });
    // Pruned rows never force the strip open.
    expect(nextStripState(collapsed, 0)).toEqual({ seenCompletions: 0, collapsed: true });
    // A fresh mount with a completion is open; with active work only it is not.
    expect(nextStripState({ seenCompletions: 0, collapsed: true }, 1)).toEqual({ seenCompletions: 1, collapsed: false });
    expect(nextStripState({ seenCompletions: 0, collapsed: true }, 0)).toEqual({ seenCompletions: 0, collapsed: true });
  });
});

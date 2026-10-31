import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RightPanelSummary } from "@/components/RightPanel";
import { encodeRunStatus } from "@/lib/run-status-copy";

function render(status: string, selectedSession: string | null = "session-1") {
  return renderToStaticMarkup(
    <RightPanelSummary
      selectedSession={selectedSession}
      sessionTitle="Hosted delegation"
      activeRun="run-1"
      status={status}
      permissionMode="ask"
    />,
  );
}

describe("RightPanel summary status", () => {
  it("renders the run status through the copy layer, not the wire form", () => {
    const html = render(encodeRunStatus("running_tool", "bash"));
    expect(html).toContain("right-panel-summary");
    expect(html).toContain("Running: bash…");
    expect(html).not.toContain("\u001f");
    expect(html).not.toContain("running_tool");
  });

  it("translates a plain status code and falls back to idle", () => {
    expect(render("interrupted")).toContain("Status: Interrupted");
    expect(render("")).toContain("Status: idle");
  });

  it("shows the empty state without a session", () => {
    const html = render("running", null);
    expect(html).toContain("No session selected");
    expect(html).not.toContain("Status:");
  });
});

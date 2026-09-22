import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RunInspectorBody } from "@/components/RunInspectorPanel";
import type { RunPromptComposition } from "@/hooks/useRunPromptComposition";

// The panel is read-only and prop-driven, so its states are pinned by rendering
// them directly. Keys are echoed so the assertions read as behaviour, not copy.
function t(key: string): string {
  return key;
}

function composition(overrides: Partial<RunPromptComposition> = {}): RunPromptComposition {
  return {
    runId: "run-1",
    version: 1,
    platformVersion: "hosted-v1",
    digest: "base-digest-0123456789",
    sections: [
      { id: "base", kind: "base", source: "agent_profile", bytes: 512, digest: "aaaa1111bbbb2222" },
      { id: "role.definition", kind: "role", source: "role:analyst@3", bytes: 2048, digest: "cccc3333dddd4444" },
      { id: "skill.preload.report", kind: "skill_preload", source: "role:analyst@3", bytes: 1024, digest: "eeee5555ffff6666", skillId: "report" },
    ],
    sectionsDigest: "sections-digest",
    composedDigest: "composed-digest",
    prompt: "base\n\n<role_definition>\nReport.\n</role_definition>",
    recorded: true,
    ...overrides,
  };
}

describe("RunInspectorBody", () => {
  it("prompts for a Run when nothing is targeted", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId={null} composition={null} loading={false} error={null} t={t} />);
    expect(html).toContain("inspector.noRun");
  });

  it("renders every recorded section with its provenance and size", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);

    expect(html).toContain("data-prompt-section=\"base\"");
    expect(html).toContain("data-prompt-section=\"role\"");
    expect(html).toContain("data-prompt-section=\"skill_preload\"");
    expect(html).toContain("role:analyst@3");
    expect(html).toContain("role.definition");
    expect(html).toContain("2.0 KiB");
    // The full digest stays available even though the row shows a prefix.
    expect(html).toContain("aaaa1111bbbb2222");
  });

  it("keeps the frozen prompt collapsed until the reader opens it", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);
    expect(html).toContain("inspector.prompt");
    expect(html).not.toContain("data-run-prompt");
    expect(html).not.toContain("Report.");
  });

  it("states that the record is frozen rather than re-derived", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);
    expect(html).toContain("inspector.frozen");
  });

  it("renders an unrecorded Run as not recorded instead of an empty prompt", () => {
    const html = renderToStaticMarkup(<RunInspectorBody
      runId="run-old" loading={false} error={null} t={t}
      composition={composition({ sections: [], sectionsDigest: "", composedDigest: "", prompt: "", recorded: false })}
    />);

    expect(html).toContain("inspector.unrecorded");
    expect(html).not.toContain("inspector.composition");
    expect(html).not.toContain("data-run-prompt");
    // The base-prompt digest is still part of the record.
    expect(html).toContain("inspector.baseDigest");
  });

  it("surfaces a fetch failure instead of rendering a misleading empty record", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={null} loading={false} error="HTTP 500" t={t} />);
    expect(html).toContain("HTTP 500");
    expect(html).not.toContain("inspector.unrecorded");
  });

  it("shows a loading state before the first response", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={null} loading error={null} t={t} />);
    expect(html).toContain("inspector.loading");
  });
});

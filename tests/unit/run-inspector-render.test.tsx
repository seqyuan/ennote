import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RunInspectorBody } from "@/components/RunInspectorPanel";
import type { RunMCPFrozen } from "@/hooks/useRunMCP";
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
    skillCatalogState: "materialized",
    skillCatalogDigest: "catalog-digest",
    recorded: true,
    ...overrides,
  };
}

function mcpSurface(): RunMCPFrozen {
  return {
    runId: "run-1",
    servers: [
      {
        id: "srv-1", bindingId: "binding-a", bindingRevision: 2, profileVersionId: "profile-a@v000001",
        configDigest: "config-a", negotiatedProtocol: "2025-06-18", serverIdentityDigest: "identity-a",
        catalogDigest: "catalog-a", required: true, unavailableReason: "",
        tools: [
          { remoteName: "search", exposedName: "bio__search", description: "Search", riskClass: "external", sourceKind: "managed", schemaDigest: "d1" },
        ],
      },
      {
        id: "srv-2", bindingId: "binding-b", bindingRevision: 1, profileVersionId: "profile-b@v000001",
        configDigest: "config-b", negotiatedProtocol: "", serverIdentityDigest: "", catalogDigest: "",
        required: false, unavailableReason: "connection refused", tools: [],
      },
    ],
  };
}

describe("RunInspectorBody MCP surface", () => {
  it("names each frozen server with its tools and revision", () => {
    const html = renderToStaticMarkup(<RunInspectorBody
      runId="run-1" composition={composition()} mcp={mcpSurface()} loading={false} error={null} t={t} />);

    expect(html).toContain("data-mcp-server=\"binding-a\"");
    expect(html).toContain("bio__search");
    expect(html).toContain("external");
    expect(html).toContain("rev 2");
  });

  it("explains an unavailable server instead of showing it as absent", () => {
    const html = renderToStaticMarkup(<RunInspectorBody
      runId="run-1" composition={composition()} mcp={mcpSurface()} loading={false} error={null} t={t} />);

    expect(html).toContain("data-mcp-server=\"binding-b\"");
    expect(html).toContain("connection refused");
    expect(html).toContain("inspector.mcpUnavailable");
  });

  it("states that no server was frozen rather than rendering an empty block", () => {
    const html = renderToStaticMarkup(<RunInspectorBody
      runId="run-1" composition={composition()} loading={false} error={null} t={t}
      mcp={{ runId: "run-1", servers: [] }} />);

    expect(html).toContain("inspector.mcpNone");
  });

  it("omits the MCP block entirely when the surface was not requested", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);
    expect(html).not.toContain("inspector.mcp");
  });
});

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

  it("explains why a catalog section is present or absent", () => {
    const recorded = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);
    expect(recorded).toContain("inspector.skillCatalog");
    expect(recorded).toContain("inspector.catalog.materialized");

    // A Run that skipped the catalog says so, instead of looking like a Run
    // whose catalog happened to be empty.
    const skipped = renderToStaticMarkup(<RunInspectorBody runId="run-1" loading={false} error={null} t={t}
      composition={composition({ skillCatalogState: "disabled", skillCatalogDigest: "" })} />);
    expect(skipped).toContain("inspector.catalog.disabled");
    expect(skipped).not.toContain("inspector.catalog.materialized");

    // An unrecorded Run claims nothing about the catalog.
    const unrecorded = renderToStaticMarkup(<RunInspectorBody runId="run-old" loading={false} error={null} t={t}
      composition={composition({
        sections: [], sectionsDigest: "", composedDigest: "", prompt: "",
        skillCatalogState: "", skillCatalogDigest: "", recorded: false,
      })} />);
    expect(unrecorded).not.toContain("inspector.skillCatalog");
  });

  it("states that the record is frozen rather than re-derived", () => {
    const html = renderToStaticMarkup(
      <RunInspectorBody runId="run-1" composition={composition()} loading={false} error={null} t={t} />);
    expect(html).toContain("inspector.frozen");
  });

  it("renders an unrecorded Run as not recorded instead of an empty prompt", () => {
    const html = renderToStaticMarkup(<RunInspectorBody
      runId="run-old" loading={false} error={null} t={t}
      composition={composition({
        sections: [], sectionsDigest: "", composedDigest: "", prompt: "",
        skillCatalogState: "", skillCatalogDigest: "", recorded: false,
      })}
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

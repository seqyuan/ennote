import { describe, expect, expectTypeOf, it } from "vitest";
import type { components, operations } from "@/lib/worker-api.gen";

type InspectRequest = components["schemas"]["PackInspectRequest"];
type InspectResult = components["schemas"]["PackInspectResult"];
type PackMCP = components["schemas"]["PackMCP"];

describe("Agent Pack inspect contract", () => {
  it("generates the read-only inspect request, result, and operation types", () => {
    const request: InspectRequest = {
      source: { kind: "path", path: "/tmp/cell-qc.enpack" },
      includeDocuments: false,
    };
    // The path-only source is deliberate: S1 has no multipart, URL, or git source.
    expectTypeOf(request.source.kind).toEqualTypeOf<"path">();
    expectTypeOf<InspectResult["signed"]>().toEqualTypeOf<boolean>();
    expectTypeOf<InspectResult["manifestDigest"]>().toEqualTypeOf<string>();
    expectTypeOf<InspectResult["flows"][number]["format"]>().toEqualTypeOf<"graph">();
    expectTypeOf<InspectResult["heroes"][number]["scope"]>().toEqualTypeOf<"flow" | "global">();
    // A vendored skill declares where it installs; an external ref has no
    // install target and therefore omits scope.
    expectTypeOf<InspectResult["skills"][number]["scope"]>().toEqualTypeOf<
      "flow" | "global" | undefined
    >();
    expectTypeOf<InspectResult["signature"]["verified"]>().toEqualTypeOf<boolean>();
    // External MCP profile refs are declared, never resolved: the result and the
    // capability envelope both expose them as plain ref strings.
    expectTypeOf<InspectResult["mcp"]>().toEqualTypeOf<PackMCP[]>();
    expectTypeOf<PackMCP["ref"]>().toEqualTypeOf<string>();
    expectTypeOf<InspectResult["capabilityEnvelope"]["requiresMcp"]>().toEqualTypeOf<string[]>();
    expectTypeOf<operations["inspectPack"]["requestBody"]["content"]["application/json"]>()
      .toMatchTypeOf<InspectRequest>();

    expect(request.source.path).toBe("/tmp/cell-qc.enpack");
  });

  // The generated file is not asserted as text here: ci.yml regenerates it and
  // fails on any diff, and the expectTypeOf block above already fails to compile
  // if the contract loses an operation or a field. String matching the generated
  // file would only prove the generator ran.
});

describe("Agent Pack install contract", () => {
  it("generates the install request, plan, and state types", () => {
    const request: components["schemas"]["PackInstallRequest"] = {
      source: { kind: "path", path: "/home/me/sources/cell-qc" },
      expectedDigest: `sha256:${"a".repeat(64)}`,
      dryRun: true,
      renames: { flows: { "qc-review": "cell-qc-review" }, heroes: { "qc-reviewer": "cell-qc-reviewer" } },
    };
    expectTypeOf(request.source.kind).toEqualTypeOf<"path">();
    expectTypeOf<components["schemas"]["PackRenames"]["flows"]>().toEqualTypeOf<
      Record<string, string> | undefined
    >();

    const plan: components["schemas"]["PackInstallPlan"] = {
      planVersion: 1,
      pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
      manifestDigest: `sha256:${"a".repeat(64)}`,
      packDigest: `sha256:${"b".repeat(64)}`,
      actions: [{ kind: "graph", storeId: "qc-review", action: "create" }],
      blockers: [{ code: "pack_flow_format_unsupported", severity: "error", message: "needs S2b" }],
      warnings: [{ code: "pack_flow_renamed", severity: "info", message: "renamed" }],
      commitPoint: "state_file",
        };
    expectTypeOf(plan.commitPoint).toEqualTypeOf<"state_file">();
    expectTypeOf<components["schemas"]["PackAction"]["kind"]>().toEqualTypeOf<
      "graph" | "flow" | "graph_hero" | "hero" | "hero_revision" | "skill"
    >();
    expectTypeOf<components["schemas"]["PackAction"]["action"]>().toEqualTypeOf<
      "create" | "exists_identical" | "conflict" | "publish"
    >();
    expectTypeOf<components["schemas"]["PackIssue"]["severity"]>().toEqualTypeOf<
      "error" | "warning" | "info" | undefined
    >();
    // Default-disabled is part of the contract, not a runtime detail.
    expectTypeOf<components["schemas"]["PackState"]["installState"]>().toEqualTypeOf<"installed_draft">();
    expectTypeOf<components["schemas"]["PackFlowState"]["publishedRevision"]>().toEqualTypeOf<
      string | null
    >();
    expectTypeOf<components["schemas"]["PackFlowState"]["format"]>().toEqualTypeOf<"graph">();
    expectTypeOf<components["schemas"]["PackStateSummary"]["flowCount"]>().toEqualTypeOf<number>();
    expectTypeOf<components["schemas"]["PackExternalRefs"]["skills"]>().toEqualTypeOf<string[]>();
    expectTypeOf<operations["installPack"]["requestBody"]["content"]["application/json"]>()
      .toMatchTypeOf<components["schemas"]["PackInstallRequest"]>();
    // The list envelope keeps `data` optional in `allOf`, so assert the item type
    // that the CLI consumes through its own narrow response guard.
    expectTypeOf<NonNullable<operations["listPacks"]["responses"][200]["content"]["application/json"]["data"]>>()
      .toEqualTypeOf<components["schemas"]["PackStateSummary"][]>();
  });

  // See the note above: the contract-to-client drift is ci.yml's
  // `generate:api` + `git diff --exit-code`, and the type assertions are the
  // contract-level expectations.
});

import fs from "node:fs";
import path from "node:path";
import { describe, expect, expectTypeOf, it } from "vitest";
import type { components, operations } from "@/lib/worker-api.gen";

const root = path.resolve(import.meta.dirname, "../..");

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
    expectTypeOf<InspectResult["flows"][number]["format"]>().toEqualTypeOf<"flow" | "graph">();
    expectTypeOf<InspectResult["roles"][number]["scope"]>().toEqualTypeOf<"flow" | "global">();
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

  it("keeps the generated client in step with the OpenAPI contract", () => {
    const generated = fs.readFileSync(path.join(root, "lib/worker-api.gen.ts"), "utf8");
    expect(generated).toContain('"/v1/packs/inspect"');
    expect(generated).toContain("inspectPack: {");
    expect(generated).toContain("PackInspectRequest: {");
    expect(generated).toContain("PackInspectResult: {");
    expect(generated).toContain("PackCapabilityEnvelope: {");
    expect(generated).toContain("PackMCP: {");
    expect(generated).toContain("requiresMcp: string[];");

    const contract = fs.readFileSync(path.join(root, "contracts/openapi.yaml"), "utf8");
    expect(contract).toContain("/v1/packs/inspect:");
    expect(contract).toContain("operationId: inspectPack");
  });
});

describe("Agent Pack install contract", () => {
  it("generates the install request, plan, and state types", () => {
    const request: components["schemas"]["PackInstallRequest"] = {
      source: { kind: "path", path: "/home/me/sources/cell-qc" },
      expectedDigest: `sha256:${"a".repeat(64)}`,
      dryRun: true,
      renames: { flows: { "qc-review": "cell-qc-review" }, roles: { "qc-reviewer": "cell-qc-reviewer" } },
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
      willPublishFlowRevision: false,
    };
    expectTypeOf(plan.commitPoint).toEqualTypeOf<"state_file">();
    expectTypeOf(plan.willPublishFlowRevision).toEqualTypeOf<boolean>();
    expectTypeOf<components["schemas"]["PackAction"]["kind"]>().toEqualTypeOf<
      "graph" | "flow" | "graph_role" | "role" | "role_revision" | "skill"
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

  it("keeps the generated client in step with the install contract", () => {
    const generated = fs.readFileSync(path.join(root, "lib/worker-api.gen.ts"), "utf8");
    expect(generated).toContain('"/v1/packs/install"');
    expect(generated).toContain('"/v1/packs"');
    expect(generated).toContain('"/v1/packs/{publisher}/{name}"');
    expect(generated).toContain("installPack: {");
    expect(generated).toContain("listPacks: {");
    expect(generated).toContain("removePack: {");
    expect(generated).toContain("PackInstallRequest: {");
    expect(generated).toContain("PackRenames: {");
    expect(generated).toContain("PackInstallPlan: {");
    expect(generated).toContain("PackAction: {");
    expect(generated).toContain("PackIssue: {");
    expect(generated).toContain("PackState: {");
    expect(generated).toContain("PackStateSummary: {");

    const contract = fs.readFileSync(path.join(root, "contracts/openapi.yaml"), "utf8");
    expect(contract).toContain("/v1/packs/install:");
    expect(contract).toContain("operationId: installPack");
    expect(contract).toContain("operationId: listPacks");
    expect(contract).toContain("operationId: removePack");
    expect(contract).toContain("/v1/packs/{publisher}/{name}:");
  });
});

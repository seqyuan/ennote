import fs from "node:fs";
import path from "node:path";
import { describe, expect, expectTypeOf, it } from "vitest";
import type { components, operations } from "@/lib/worker-api.gen";

const root = path.resolve(import.meta.dirname, "../..");

type InspectRequest = components["schemas"]["PackInspectRequest"];
type InspectResult = components["schemas"]["PackInspectResult"];

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
    expectTypeOf<InspectResult["signature"]["verified"]>().toEqualTypeOf<boolean>();
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

    const contract = fs.readFileSync(path.join(root, "contracts/openapi.yaml"), "utf8");
    expect(contract).toContain("/v1/packs/inspect:");
    expect(contract).toContain("operationId: inspectPack");
  });
});

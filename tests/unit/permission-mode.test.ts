import { describe, expect, it } from "vitest";

import { permissionModeForPolicyID, permissionPolicyID, withPermissionConfig, withRunConfig } from "../../lib/permission-mode";

describe("per-turn permission mode", () => {
  it("prefers the stable built-in profile for the selected mode", () => {
    const profiles = [
      { id: "custom", kind: "tool", status: "active", config: { mode: "discuss" } },
      { id: "builtin-tool-discuss-v1", kind: "tool", status: "active", config: { mode: "discuss" } },
      { id: "builtin-tool-ask-v1", kind: "tool", status: "active", config: { mode: "ask" } },
      { id: "inactive", kind: "tool", status: "inactive", config: { mode: "auto" } },
    ];
    expect(permissionPolicyID(profiles, "discuss")).toBe("builtin-tool-discuss-v1");
    expect(permissionPolicyID(profiles, "ask")).toBe("builtin-tool-ask-v1");
    expect(permissionPolicyID(profiles, "auto")).toBeUndefined();
  });

  it("restores the frozen mode from a built-in profile ID", () => {
    expect(permissionModeForPolicyID([], "builtin-tool-ask-v1")).toBe("ask");
    expect(permissionModeForPolicyID([], "custom")).toBeUndefined();
  });

  it("adds the frozen policy and optional model selection to a turn payload", () => {
    expect(withPermissionConfig({ text: "hello" }, "builtin-tool-auto-v1")).toEqual({
      text: "hello",
      config: { toolPolicyProfileId: "builtin-tool-auto-v1" },
    });
    expect(withRunConfig({ text: "hello" }, "builtin-tool-auto-v1", "model-1")).toEqual({
      text: "hello",
      config: { toolPolicyProfileId: "builtin-tool-auto-v1", modelProfileId: "model-1" },
    });
  });
});

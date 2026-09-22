import { describe, expect, it } from "vitest";

import { permissionModeForPolicyID, permissionPolicyID, withPermissionConfig, withRunConfig } from "../../lib/permission-mode";

describe("per-turn permission mode", () => {
  it("prefers the versioned built-in profile for the selected mode", () => {
    const profiles = [
      { id: "custom", kind: "tool", status: "active", config: { mode: "discuss" } },
      { id: "builtin-tool-discuss-v3", kind: "tool", status: "active", config: { mode: "discuss" } },
      { id: "builtin-tool-ask-v1", kind: "tool", status: "active", config: { mode: "ask" } },
      { id: "inactive", kind: "tool", status: "inactive", config: { mode: "auto" } },
    ];
    expect(permissionPolicyID(profiles, "discuss")).toBe("builtin-tool-discuss-v3");
    expect(permissionPolicyID(profiles, "ask")).toBe("builtin-tool-ask-v1");
    expect(permissionPolicyID(profiles, "auto")).toBeUndefined();
  });

  // The Worker ships the Discuss builtin at v3 and Ask/Auto at v1, so selection
  // must not pin a version number.
  it("matches any versioned built-in id for the mode", () => {
    const profiles = [
      { id: "custom-discuss", kind: "tool", status: "active", config: { mode: "discuss" } },
      { id: "builtin-tool-discuss-v7", kind: "tool", status: "active", config: { mode: "discuss" } },
    ];
    expect(permissionPolicyID(profiles, "discuss")).toBe("builtin-tool-discuss-v7");
  });

  // A custom same-mode policy is still used when no builtin exists, but the
  // non-permission tool policies must never be selectable.
  it("never selects a non-permission tool policy", () => {
    const profiles = [
      { id: "builtin-tool-allow-existing-v1", kind: "tool", status: "active", config: { mode: "allow_existing_behavior" } },
      { id: "custom-restricted", kind: "tool", status: "active", config: { mode: "restricted" } },
    ];
    expect(permissionPolicyID(profiles, "discuss")).toBeUndefined();
    expect(permissionPolicyID(profiles, "ask")).toBeUndefined();
    expect(permissionPolicyID(profiles, "auto")).toBeUndefined();
  });

  it("falls back to a custom same-mode policy when no builtin exists", () => {
    const profiles = [
      { id: "custom-ask", kind: "tool", status: "active", config: { mode: "ask" } },
    ];
    expect(permissionPolicyID(profiles, "ask")).toBe("custom-ask");
  });

  it("restores the frozen mode from a built-in profile ID", () => {
    expect(permissionModeForPolicyID([], "builtin-tool-discuss-v3")).toBe("discuss");
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

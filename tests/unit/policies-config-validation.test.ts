import { describe, expect, it } from "vitest";

import { validatePolicyConfig } from "@/components/settings/PoliciesSettings";

// The policy editor is a bare textarea: submit used to be the first reader of
// the JSON, so a typo surfaced as one raw error line after the round trip.
// Validation runs on blur and on submit through the same function.
describe("validatePolicyConfig", () => {
  it("accepts a configuration object", () => {
    expect(validatePolicyConfig('{"mode":"restricted"}')).toBeNull();
  });

  it("treats an empty value as valid so the field is not flagged before typing", () => {
    expect(validatePolicyConfig("")).toBeNull();
    expect(validatePolicyConfig("   \n")).toBeNull();
  });

  it("reports the text the parser rejected", () => {
    const error = validatePolicyConfig('{"mode":}');

    expect(error).toContain("Invalid JSON:");
    expect(error).toContain('{"mode":}');
  });

  it("rejects JSON that is not an object", () => {
    expect(validatePolicyConfig("[]")).toBe("Configuration must be a JSON object.");
    expect(validatePolicyConfig("null")).toBe("Configuration must be a JSON object.");
    expect(validatePolicyConfig("5")).toBe("Configuration must be a JSON object.");
  });
});

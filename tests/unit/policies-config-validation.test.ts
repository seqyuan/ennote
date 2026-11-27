import { describe, expect, it } from "vitest";

import { validatePolicyConfig } from "@/components/settings/PoliciesSettings";
import { translate } from "@/lib/messages";

// The policy editor is a bare textarea: submit used to be the first reader of
// the JSON, so a typo surfaced as one raw error line after the round trip.
// Validation runs on blur and on submit through the same function, and it
// answers with a message key rather than a sentence — the same rule then reads
// in either locale, and this test pins the rule instead of the English.
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

    expect(error?.key).toBe("settings.policies.error.invalidJson");
    expect(error).toMatchObject({ params: { detail: expect.stringContaining('{"mode":}') } });
  });

  it("rejects JSON that is not an object", () => {
    for (const raw of ["[]", "null", "5", '"text"']) {
      expect(validatePolicyConfig(raw)).toEqual({ key: "settings.policies.error.notObject" });
    }
  });

  it("resolves the same rule in both locales", () => {
    expect(translate("en", "settings.policies.error.notObject")).toBe("Configuration must be a JSON object.");
    expect(translate("zh-CN", "settings.policies.error.notObject")).toBe("配置必须是一个 JSON 对象。");
    expect(translate("en", "settings.policies.error.invalidJson", { detail: "boom" })).toBe("Invalid JSON: boom");
    expect(translate("zh-CN", "settings.policies.error.invalidJson", { detail: "boom" })).toBe("JSON 无效：boom");
  });
});

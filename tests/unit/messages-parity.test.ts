import { describe, expect, it } from "vitest";

import { messages, placeholders, translate } from "../../lib/messages";

describe("message dictionaries", () => {
  it("has the same keys in English and Simplified Chinese", () => {
    const en = Object.keys(messages.en).sort();
    const zh = Object.keys(messages["zh-CN"]).sort();
    expect(zh).toEqual(en);
  });

  it("has no empty translations", () => {
    for (const locale of ["en", "zh-CN"] as const) {
      for (const [key, value] of Object.entries(messages[locale])) {
        expect(value.trim(), `${locale}:${key}`).not.toBe("");
      }
    }
  });

  // A hand-written `.replace("{count}", ...)` chain silently renders a literal
  // "{count}" when a translation drops the placeholder, and silently drops the
  // value when a translation renames it. Interpolation moved into translate();
  // these two pin the property that makes it safe.
  it("uses the same placeholders in every locale", () => {
    for (const [key, english] of Object.entries(messages.en)) {
      const chinese = messages["zh-CN"][key];
      expect(placeholders(chinese), `zh-CN:${key}`).toEqual(placeholders(english));
    }
  });

  it("fills placeholders and leaves unknown tokens alone", () => {
    expect(translate("en", "conversation.delegation.active", { count: 3 })).toContain("3");
    expect(translate("en", "conversation.delegation.active")).toContain("{count}");
    // A missing parameter must not blank the token: that would hide the bug.
    expect(translate("en", "conversation.delegation.active", { other: 1 })).toContain("{count}");
    // Numbers keep their value, and a token is not re-scanned after substitution.
    expect(translate("en", "context.aria", { percent: 0 })).toContain("0");
    expect(translate("en", "context.aria", { percent: "{count}" })).toContain("{count}");
  });
});

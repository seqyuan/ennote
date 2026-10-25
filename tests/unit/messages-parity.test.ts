import { describe, expect, it } from "vitest";

import { messages } from "../../lib/messages";

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
});

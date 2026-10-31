import { describe, expect, it } from "vitest";

import { messages, translate } from "../../lib/messages";
import {
  RUN_STATUS_RECONNECTING,
  STATUS_KEYS,
  decodeRunStatus,
  encodeRunStatus,
  runStatusText,
} from "../../lib/run-status-copy";

const en = (key: string) => translate("en", key);
const zh = (key: string) => translate("zh-CN", key);
const SEP = "\u001f";

describe("run status copy", () => {
  it("keeps tool names and compaction detail on stable codes", () => {
    expect(decodeRunStatus(encodeRunStatus("running_tool", "bash")).code).toBe("running_tool");
    expect(runStatusText(encodeRunStatus("running_tool", "bash"), en)).toBe("Running: bash…");
    expect(runStatusText(encodeRunStatus("compaction_retry_wait", "250"), zh)).toContain("250");
    expect(decodeRunStatus(encodeRunStatus(RUN_STATUS_RECONNECTING)).code).toBe("reconnecting");
    expect(STATUS_KEYS[RUN_STATUS_RECONNECTING]).toBeTruthy();
  });

  it("has English and Chinese copy for every mapped status", () => {
    for (const [code, key] of Object.entries(STATUS_KEYS)) {
      expect(messages.en[key], `${code} -> ${key} (en)`).toBeTruthy();
      expect(messages["zh-CN"][key], `${code} -> ${key} (zh-CN)`).toBeTruthy();
      expect(messages.en[key], `${code} -> ${key}`).not.toBe(messages["zh-CN"][key]);
      // A `{detail}` slot must exist in both locales or one language drops the
      // dynamic fact the Worker encoded on the status code.
      expect(messages.en[key].includes("{detail}"), `${key} (en detail slot)`)
        .toBe(messages["zh-CN"][key].includes("{detail}"));
    }
  });

  it("renders every mapped code as copy, never as the wire form", () => {
    for (const [code, key] of Object.entries(STATUS_KEYS)) {
      for (const [locale, t] of [["en", en], ["zh-CN", zh]] as const) {
        const text = runStatusText(code, t);
        expect(text, `${locale}:${code}`).not.toContain(SEP);
        expect(text, `${locale}:${code}`).not.toBe(code);
        expect(text, `${locale}:${code}`).toBe(messages[locale][key].replace("{detail}", ""));
      }
    }
  });

  it("renders every mapped code with detail through its own copy key", () => {
    for (const [code, key] of Object.entries(STATUS_KEYS)) {
      const detail = "detail-value";
      const text = runStatusText(encodeRunStatus(code, detail), en);
      expect(text, code).not.toContain(SEP);
      expect(text, code).toBe(messages.en[key].split("{detail}").join(detail));
    }
  });

  it("returns the detail — not the encoded wire form — for an unmapped code", () => {
    const status = encodeRunStatus("future_status", "provider said no");
    expect(decodeRunStatus(status).code).toBe("future_status");
    expect(runStatusText(status, en)).toBe("provider said no");
    expect(runStatusText(status, en)).not.toContain(SEP);
    // Without a detail an unmapped code degrades to readable text, not to the
    // raw enum with underscores.
    expect(runStatusText(encodeRunStatus("future_status"), en)).toBe("future status");
  });

  it("treats $ replacement patterns in the detail as literal text", () => {
    const detail = "patch $& and $` and $' stay literal";
    expect(runStatusText(encodeRunStatus("running_tool", detail), en)).toBe(`Running: ${detail}…`);
    expect(runStatusText(encodeRunStatus("model_retry", detail), zh)).toContain(detail);
    expect(runStatusText(encodeRunStatus("model_retry", detail), zh)).not.toContain("{detail}");
  });
});

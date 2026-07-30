import { describe, expect, it } from "vitest";

import {
  boundedToolOutput,
  classifyDisplayRisk,
  defaultToolExpanded,
  redactToolArguments,
  summarizeToolCall,
} from "../../lib/tool-presentation";

describe("tool presentation", () => {
  it("classifies known tools and treats unknown tools as sensitive", () => {
    expect(classifyDisplayRisk("read")).toBe("read_only");
    expect(classifyDisplayRisk("write")).toBe("local_write");
    expect(classifyDisplayRisk("bash")).toBe("shell");
    expect(classifyDisplayRisk("http_request")).toBe("external");
    expect(classifyDisplayRisk("unregistered_tool")).toBe("sensitive");
  });

  it("recursively redacts credential-like keys without changing safe values", () => {
    expect(redactToolArguments({
      path: "/workspace/data.csv",
      auth: { apiKey: "abc", nested: { password: "pw", value: 3 } },
      headers: { Authorization: "Bearer secret", Accept: "application/json" },
    })).toEqual({
      path: "/workspace/data.csv",
      auth: { apiKey: "[redacted]", nested: { password: "[redacted]", value: 3 } },
      headers: { Authorization: "[redacted]", Accept: "application/json" },
    });
  });

  it("creates concise summaries for file and shell tools", () => {
    expect(summarizeToolCall("read", { path: "/workspace/data.csv", offset: 20, limit: 40 })).toMatchObject({
      label: "Read file", target: "/workspace/data.csv", detail: "lines 20-59",
    });
    expect(summarizeToolCall("bash", { command: "npm test -- tests/unit/tool-presentation.test.ts" })).toMatchObject({
      label: "Run command", target: "npm test -- tests/unit/tool-presentation.test.ts",
    });
  });

  it("bounds long output and uses risk and state for default disclosure", () => {
    expect(boundedToolOutput("x".repeat(50), 20)).toBe("xxxxxxxxxxxxxxxxxxxx\n[30 characters omitted]");
    expect(defaultToolExpanded("read_only", "completed")).toBe(false);
    expect(defaultToolExpanded("local_write", "completed")).toBe(true);
    expect(defaultToolExpanded("read_only", "failed")).toBe(true);
    expect(defaultToolExpanded("shell", "running")).toBe(true);
  });
});

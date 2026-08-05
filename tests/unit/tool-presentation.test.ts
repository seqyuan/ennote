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
    expect(classifyDisplayRisk("todo")).toBe("read_only");
    expect(classifyDisplayRisk("write")).toBe("local_write");
    expect(classifyDisplayRisk("bash")).toBe("shell");
    expect(classifyDisplayRisk("http_request")).toBe("external");
    expect(classifyDisplayRisk("delegate_roles")).toBe("delegation");
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

  it("summarizes delegated Role assignments", () => {
    expect(summarizeToolCall("delegate_roles", { delegations: [
      { roleHandle: "explorer", assignment: "Inspect files" },
      { roleHandle: "reviewer", assignment: "Review findings" },
    ] })).toEqual({
      label: "Delegate roles", target: "2 assignments", detail: "@explorer, @reviewer",
    });
  });

  it("classifies and summarizes todo tool calls", () => {
    expect(summarizeToolCall("todo", {
      todos: [
        { content: "inspect data", status: "completed" },
        { content: "write report", status: "in_progress" },
      ],
    })).toMatchObject({
      label: "Update task list",
      target: "1/2 completed",
    });
    expect(summarizeToolCall("todo", { todos: [] })).toMatchObject({
      label: "Update task list",
      target: "0/0 completed",
    });
    expect(summarizeToolCall("todo", {
      todos: [
        { content: "a", status: "completed" },
        { content: "b", status: "completed" },
        { content: "c", status: "completed" },
      ],
    })).toMatchObject({
      label: "Update task list",
      target: "3/3 completed",
    });
    expect(classifyDisplayRisk("todo")).toBe("read_only");
  });

  it("bounds long output and uses risk and state for default disclosure", () => {
    expect(boundedToolOutput("x".repeat(50), 20)).toBe("xxxxxxxxxxxxxxxxxxxx\n[30 characters omitted]");
    expect(defaultToolExpanded("read_only", "completed")).toBe(false);
    expect(defaultToolExpanded("local_write", "completed")).toBe(true);
    expect(defaultToolExpanded("read_only", "failed")).toBe(true);
    expect(defaultToolExpanded("shell", "running")).toBe(true);
  });
});

describe("MCP tool presentation", () => {
  it("splits {server}__{tool} into server and remote label", () => {
    expect(summarizeToolCall("pubmed__search_articles", { query: "cancer" })).toMatchObject({
      label: "search_articles",
      target: "MCP · pubmed",
    });
    expect(summarizeToolCall("geo__geocode", {})).toMatchObject({
      label: "geocode",
      target: "MCP · geo",
    });
  });

  it("falls back for non-MCP unknown tools", () => {
    expect(summarizeToolCall("something_new", {})).toMatchObject({
      label: "something_new",
    });
  });
});

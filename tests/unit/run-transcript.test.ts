import { describe, expect, it } from "vitest";
import { transcriptEntries, transcriptSummary } from "@/lib/run-transcript";
import type { components } from "@/lib/worker-api.gen";

type Block = components["schemas"]["MessageContentBlock"];

describe("transcriptEntries", () => {
  it("keeps the model-visible order of blocks", () => {
    const blocks: Block[] = [
      { type: "thinking", text: "weighing options" },
      { type: "text", text: "Looking at the data." },
      { type: "tool_call", toolCall: { id: "call-1", name: "read", arguments: { path: "a.csv" } } },
      { type: "tool_result", toolResult: { toolCallId: "call-1", toolName: "read", content: "a,b", isError: false } },
      { type: "text", text: "Done." },
    ];

    expect(transcriptEntries(blocks)).toEqual([
      { kind: "thinking", text: "weighing options" },
      { kind: "text", text: "Looking at the data." },
      { kind: "tool_call", callId: "call-1", name: "read", arguments: "{\n  \"path\": \"a.csv\"\n}" },
      { kind: "tool_result", callId: "call-1", name: "read", text: "a,b", isError: false },
      { kind: "text", text: "Done." },
    ]);
  });

  it("marks a failed tool result so a reader cannot mistake it for output", () => {
    const blocks: Block[] = [
      { type: "tool_result", toolResult: { toolCallId: "c", toolName: "bash", content: "exit 1", isError: true } },
    ];
    expect(transcriptEntries(blocks)).toEqual([
      { kind: "tool_result", callId: "c", name: "bash", text: "exit 1", isError: true },
    ]);
  });

  it("describes an image without inventing its bytes", () => {
    const blocks: Block[] = [
      { type: "image", image: { artifactId: "art-1", mimeType: "image/png", sha256: "s", width: 640, height: 480 } },
      { type: "image_description", imageDescription: { artifactId: "art-1", text: "a scatter plot", modelId: "m", promptVersion: "v1" } },
    ];
    expect(transcriptEntries(blocks)).toEqual([
      { kind: "image", width: 640, height: 480 },
      { kind: "text", text: "a scatter plot" },
    ]);
  });

  it("records an unrecognized block rather than dropping it", () => {
    const blocks = [{ type: "future_block", payload: 1 } as unknown as Block];
    expect(transcriptEntries(blocks)).toEqual([{ kind: "unknown", type: "future_block" }]);
  });

  it("tolerates a malformed block without throwing", () => {
    const blocks = [{ type: "tool_call" } as unknown as Block];
    expect(transcriptEntries(blocks)).toEqual([
      { kind: "tool_call", callId: "", name: "tool", arguments: undefined },
    ]);
  });

  it("returns nothing for an empty message", () => {
    expect(transcriptEntries([])).toEqual([]);
  });
});

describe("transcriptSummary", () => {
  it("names the tools the message called, in order and de-duplicated", () => {
    const blocks: Block[] = [
      { type: "tool_call", toolCall: { id: "1", name: "read", arguments: {} } },
      { type: "tool_call", toolCall: { id: "2", name: "read", arguments: {} } },
      { type: "tool_call", toolCall: { id: "3", name: "bash", arguments: {} } },
      { type: "text", text: "text is not a tool" },
    ];
    expect(transcriptSummary(blocks)).toBe("read, bash");
  });

  it("is empty when the message called nothing", () => {
    expect(transcriptSummary([{ type: "text", text: "hi" }])).toBe("");
  });
});

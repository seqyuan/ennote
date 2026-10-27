import { describe, expect, it } from "vitest";

import { messages } from "../../lib/messages";
import { runStatusMessageKey } from "../../lib/run-status-copy";

describe("run status copy", () => {
  it("maps stable run status codes to localized messages", () => {
    expect(runStatusMessageKey("Waiting for your answer")).toBe("run.status.waitingAnswer");
    expect(runStatusMessageKey("Waiting for approval")).toBe("run.status.waitingApproval");
    expect(runStatusMessageKey("Answer submitted. Resuming…")).toBe("run.status.answerSubmitted");
    expect(runStatusMessageKey("Running…")).toBeUndefined();
  });

  it("has English and Chinese copy for every mapped status", () => {
    for (const key of [
      "run.status.waitingAnswer", "run.status.waitingApproval",
      "run.status.answerSubmitted", "run.status.answerQuestionFirst",
    ]) {
      expect(messages.en[key]).toBeTruthy();
      expect(messages["zh-CN"][key]).toBeTruthy();
    }
  });
});

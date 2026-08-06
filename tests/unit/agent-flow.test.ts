import { describe, expect, it } from "vitest";

import { draftToYAML, emptyTask, flowToDraft, suggestedBudget, variableChips, type FlowDraft } from "../../lib/agent-flow";

function sampleDraft(): FlowDraft {
  const producer = { ...emptyTask("producer"), role: "flow-worker@1", skills: ["go-dev"], goal: "Implement {inputs.target}", budgetTokens: "50000" };
  const reviewer = { ...emptyTask("reviewer"), role: "flow-worker@1", goal: "Review {task.producer.output.changedFiles}", depends: ["producer"] };
  const accept = { ...emptyTask("accept"), kind: "terminal" as const, depends: ["reviewer"], outputPort: "report" };
  return {
    id: "go-review", description: "maker-checker",
    inputs: [{ name: "target", type: "path", required: true }],
    outputs: [{ name: "report", type: "string" }],
    maxTotalTokens: "120000",
    tasks: [producer, reviewer, accept],
  };
}

describe("Agent Flow editor YAML contract", () => {
  it("serializes the form to the snake_case contract", () => {
    const yaml = draftToYAML(sampleDraft());
    expect(yaml).toContain("schemaVersion: 1");
    expect(yaml).toContain("id: go-review");
    expect(yaml).toContain("max_total_tokens: 120000");
    expect(yaml).toContain("role: flow-worker@1");
    expect(yaml).toContain('goal: "Implement {inputs.target}"');
    expect(yaml).toContain("depends: [producer]");
    expect(yaml).not.toContain("type: check"); // no check task here
    expect(yaml).toContain("terminal: {status: success, output: report}");
    expect(yaml).not.toContain("prev.");
  });

  it("serializes check tasks with sandbox commands", () => {
    const draft = sampleDraft();
    draft.tasks.splice(1, 0, { ...emptyTask("gate"), kind: "check", command: "go test ./...", depends: ["producer"] });
    const yaml = draftToYAML(draft);
    expect(yaml).toContain("    type: check");
    expect(yaml).toContain('    command: "go test ./..."');
  });

  it("round-trips a definition through flowToDraft", () => {
    const draft = flowToDraft({
      description: "x",
      inputs: { target: { type: "path", required: true } },
      outputs: { report: { type: "string" } },
      budget: { maxTotalTokens: 99999 },
      tasks: {
        producer: { role: "flow-worker@1", skills: ["go-dev"], goal: "g", budget: { tokens: 4000 } },
        gate: { type: "check", command: "go test ./...", depends: ["producer"] },
        accept: { terminal: { status: "success", output: "report" }, depends: ["gate"] },
      },
    }, "round");
    expect(draft.id).toBe("round");
    expect(draft.maxTotalTokens).toBe("99999");
    expect(draft.tasks).toHaveLength(3);
    expect(draft.tasks[0].kind).toBe("role");
    expect(draft.tasks[1].kind).toBe("check");
    expect(draft.tasks[2].kind).toBe("terminal");
    expect(draft.tasks[1].command).toBe("go test ./...");
  });
});

describe("Agent Flow budget suggestion", () => {
  it("computes 1.25x sum of task budgets with a 10k floor", () => {
    const draft = sampleDraft();
    expect(suggestedBudget(draft)).toBe(62500);
    const empty = { ...sampleDraft(), tasks: [emptyTask("only")] };
    expect(suggestedBudget(empty)).toBe(10000);
  });
});

describe("Agent Flow variable autocomplete scope", () => {
  it("only offers inputs, previous task outputs, and flow vars — never prev.*", () => {
    const draft = sampleDraft();
    const reviewerChips = variableChips(draft.tasks[1], draft);
    expect(reviewerChips).toContain("{inputs.target}");
    expect(reviewerChips).toContain("{task.producer.output}");
    expect(reviewerChips).not.toContain("{task.accept.output}"); // future task is out of scope
    expect(reviewerChips).toContain("{flow.vars.mode}");
    for (const chip of reviewerChips) {
      expect(chip).not.toMatch(/prev\./);
    }
    // The first task has no depends, so no task chips.
    const producerChips = variableChips(draft.tasks[0], draft);
    expect(producerChips.some((chip) => chip.startsWith("{task."))).toBe(false);
  });
});

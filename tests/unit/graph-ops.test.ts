import { describe, expect, it } from "vitest";

import { applyOps, mergeQueuedOp, opToBody, sameTarget, type QueuedOp } from "@/lib/graph-ops";
import type { FlowDefinition, FlowTask } from "@/components/graphs/types";

function definition(tasks: Record<string, FlowTask> = {}): FlowDefinition {
  return {
    schemaVersion: 1,
    id: "rna-seq",
    name: "RNA-seq",
    budget: { maxTotalTokens: 120000 },
    tasks,
  };
}

const producer: FlowTask = { hero: "local/align", goal: "Align reads" };

describe("applyOps", () => {
  it("returns the same object when there is nothing to replay", () => {
    const start = definition({ producer });
    expect(applyOps(start, [])).toBe(start);
  });

  it("returns the same object when every op is already satisfied", () => {
    const start = definition({ producer });
    const ops: QueuedOp[] = [
      { graphId: "rna-seq", kind: "name", value: "RNA-seq" },
      { graphId: "rna-seq", kind: "task", id: "missing", value: null },
    ];
    expect(applyOps(start, ops)).toBe(start);
  });

  it("applies a name change", () => {
    const next = applyOps(definition(), [{ graphId: "rna-seq", kind: "name", value: "Renamed" }]);
    expect(next.name).toBe("Renamed");
    expect(next.tasks).toEqual({});
  });

  it("applies a goal edit", () => {
    const next = applyOps(definition({ producer }), [
      { graphId: "rna-seq", kind: "task", id: "producer", value: { ...producer, goal: "Align fastq" } },
    ]);
    expect(next.tasks.producer.goal).toBe("Align fastq");
  });

  it("adds a task without touching its siblings", () => {
    const start = definition({ producer });
    const next = applyOps(start, [
      { graphId: "rna-seq", kind: "task", id: "reviewer", value: { hero: "global/reviewer", goal: "Review" } },
    ]);
    expect(Object.keys(next.tasks).sort()).toEqual(["producer", "reviewer"]);
    expect(next.tasks.producer).toBe(start.tasks.producer);
  });

  it("replaces a task when two ops target it", () => {
    const next = applyOps(definition({ producer }), [
      { graphId: "rna-seq", kind: "task", id: "producer", value: { ...producer, goal: "first" } },
      { graphId: "rna-seq", kind: "task", id: "producer", value: { ...producer, goal: "second" } },
    ]);
    expect(next.tasks.producer.goal).toBe("second");
  });

  it("deletes a task and prunes it from the other tasks' depends", () => {
    const next = applyOps(
      definition({
        producer,
        reviewer: { hero: "global/reviewer", goal: "Review", depends: ["producer", "gate"] },
        gate: { type: "check", command: "true", depends: ["producer"] },
      }),
      [{ graphId: "rna-seq", kind: "task", id: "producer", value: null }],
    );
    expect(Object.keys(next.tasks).sort()).toEqual(["gate", "reviewer"]);
    expect(next.tasks.reviewer.depends).toEqual(["gate"]);
    expect(next.tasks.gate.depends).toEqual([]);
  });

  it("does not mutate the definition it was given", () => {
    const start = definition({ producer, reviewer: { hero: "global/reviewer", depends: ["producer"] } });
    const before = JSON.parse(JSON.stringify(start));
    applyOps(start, [
      { graphId: "rna-seq", kind: "name", value: "Renamed" },
      { graphId: "rna-seq", kind: "task", id: "producer", value: null },
    ]);
    expect(start).toEqual(before);
    expect(start.tasks.reviewer.depends).toEqual(["producer"]);
  });
});

describe("opToBody", () => {
  it("sends a task without a Hero payload when there is no creation intent", () => {
    const body = opToBody({ graphId: "rna-seq", kind: "task", id: "producer", value: producer });
    expect(body).toEqual({ task: { id: "producer", value: producer } });
  });

  it("carries the Hero document on the op that creates it", () => {
    const localHero = { handle: "producer", name: "Producer" };
    const body = opToBody({ graphId: "rna-seq", kind: "task", id: "producer", value: producer, localHero });
    expect(body).toEqual({ task: { id: "producer", value: producer }, localHero });
  });

  it("drops the Hero payload from a delete", () => {
    const body = opToBody({
      graphId: "rna-seq",
      kind: "task",
      id: "producer",
      value: null,
      localHero: { handle: "producer" },
    });
    expect(body).toEqual({ task: { id: "producer", value: null } });
  });

  it("maps the name and description singletons", () => {
    expect(opToBody({ graphId: "rna-seq", kind: "name", value: "n" })).toEqual({ name: "n" });
    expect(opToBody({ graphId: "rna-seq", kind: "description", value: "d" })).toEqual({ description: "d" });
  });
});

describe("sameTarget", () => {
  it("never conflates the name and description singletons", () => {
    expect(sameTarget({ graphId: "g", kind: "name", value: "a" }, { graphId: "g", kind: "description", value: "a" }))
      .toBe(false);
  });

  it("separates different tasks", () => {
    expect(sameTarget(
      { graphId: "g", kind: "task", id: "a", value: producer },
      { graphId: "g", kind: "task", id: "b", value: producer },
    )).toBe(false);
  });

  it("matches the same task and the same singleton", () => {
    expect(sameTarget(
      { graphId: "g", kind: "task", id: "a", value: producer },
      { graphId: "g", kind: "task", id: "a", value: null },
    )).toBe(true);
    expect(sameTarget({ graphId: "g", kind: "name", value: "x" }, { graphId: "g", kind: "name", value: "y" }))
      .toBe(true);
  });
});

// The four rows of plan v3 §2.2: the Hero creation intent survives later field
// edits, and a delete never inherits it.
describe("mergeQueuedOp", () => {
  const localHero = { handle: "producer" };

  it("keeps the Hero payload when a later edit overwrites the creating op", () => {
    const merged = mergeQueuedOp(
      { graphId: "g", kind: "task", id: "producer", value: producer, localHero },
      { graphId: "g", kind: "task", id: "producer", value: { ...producer, goal: "edited" } },
    );
    expect(merged).toMatchObject({ localHero, value: { goal: "edited" } });
  });

  it("does not inherit the Hero payload on a delete", () => {
    const merged = mergeQueuedOp(
      { graphId: "g", kind: "task", id: "producer", value: producer, localHero },
      { graphId: "g", kind: "task", id: "producer", value: null },
    );
    expect(merged).toEqual({ graphId: "g", kind: "task", id: "producer", value: null });
  });

  it("leaves a task op alone when the prior op was a different kind", () => {
    const next: QueuedOp = { graphId: "g", kind: "task", id: "producer", value: producer };
    expect(mergeQueuedOp({ graphId: "g", kind: "name", value: "x" }, next)).toBe(next);
  });

  it("prefers the incoming Hero payload over the prior one", () => {
    const merged = mergeQueuedOp(
      { graphId: "g", kind: "task", id: "producer", value: producer, localHero },
      { graphId: "g", kind: "task", id: "producer", value: producer, localHero: { handle: "other" } },
    );
    expect(merged).toMatchObject({ localHero: { handle: "other" } });
  });
});

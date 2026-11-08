import { describe, expect, it } from "vitest";
import { detectComposerTrigger } from "@/lib/composer-trigger";

describe("detectComposerTrigger", () => {
  it("opens at the start of the draft", () => {
    expect(detectComposerTrigger("@", 1)).toEqual({ kind: "all", query: "", start: 0 });
    expect(detectComposerTrigger("/", 1)).toEqual({ kind: "skill", query: "", start: 0 });
  });

  it("opens mid-sentence after whitespace", () => {
    // "看看这段 @sec" — the trigger belongs to the word being typed, not the line.
    expect(detectComposerTrigger("看看这段 @sec", 9)).toEqual({ kind: "all", query: "sec", start: 5 });
  });

  it("opens after punctuation", () => {
    expect(detectComposerTrigger("(see @qc)", 8)?.query).toBe("qc");
  });

  it("does not open inside an email or a word", () => {
    expect(detectComposerTrigger("mail foo@bar.com", 15)).toBeNull();
    expect(detectComposerTrigger("foo@bar", 7)).toBeNull();
  });

  it("does not open inside a path", () => {
    expect(detectComposerTrigger("src/app", 7)).toBeNull();
    expect(detectComposerTrigger("a/b", 3)).toBeNull();
    expect(detectComposerTrigger("http://x/", 9)).toBeNull();
    expect(detectComposerTrigger("C:\\x\\", 5)).toBeNull();
  });

  it("reads the canonical prefixes and their legacy aliases", () => {
    expect(detectComposerTrigger("@hero:sec", 9)).toEqual({ kind: "hero", query: "sec", start: 0 });
    expect(detectComposerTrigger("@role:sec", 9)).toEqual({ kind: "hero", query: "sec", start: 0 });
    expect(detectComposerTrigger("@graph:go", 9)).toEqual({ kind: "graph", query: "go", start: 0 });
    expect(detectComposerTrigger("/skill:cell", 11)).toEqual({ kind: "skill", query: "cell", start: 0 });
    expect(detectComposerTrigger("/template:rev", 13)).toEqual({ kind: "template", query: "rev", start: 0 });
  });

  it("treats a bare slash as a skill search", () => {
    expect(detectComposerTrigger("/cell", 5)).toEqual({ kind: "skill", query: "cell", start: 0 });
  });

  it("closes once the token is followed by a space", () => {
    expect(detectComposerTrigger("@sec ", 5)).toBeNull();
    expect(detectComposerTrigger("/cell then", 10)).toBeNull();
  });

  it("ignores an out-of-range caret", () => {
    expect(detectComposerTrigger("@sec", 99)).toBeNull();
    expect(detectComposerTrigger("@sec", -1)).toBeNull();
  });
});

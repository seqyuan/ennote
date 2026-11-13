import { describe, expect, it } from "vitest";
import { mergeSkillBindings } from "@/components/settings/HeroesSettings";
import type { SkillBinding } from "@/components/settings/HeroesSettings";

describe("mergeSkillBindings", () => {
  // Regression: re-typing the Skills field used to rebuild every binding with
  // mode "available", silently downgrading any skill authored with "preload".
  it("keeps the existing mode for ids that were already bound", () => {
    const previous: SkillBinding[] = [
      { id: "report-writing", mode: "preload" },
      { id: "code-review", mode: "available" },
    ];

    expect(mergeSkillBindings(["code-review", "report-writing"], previous)).toEqual([
      { id: "code-review", mode: "available" },
      { id: "report-writing", mode: "preload" },
    ]);
  });

  it("defaults genuinely new ids to available", () => {
    expect(mergeSkillBindings(["brand-new"], [{ id: "report-writing", mode: "preload" }])).toEqual([
      { id: "brand-new", mode: "available" },
    ]);
  });

  it("drops ids the user removed", () => {
    expect(
      mergeSkillBindings(
        ["report-writing"],
        [
          { id: "report-writing", mode: "preload" },
          { id: "code-review", mode: "available" },
        ],
      ),
    ).toEqual([{ id: "report-writing", mode: "preload" }]);
  });

  it("follows the typed order", () => {
    expect(
      mergeSkillBindings(
        ["z", "a", "m"],
        [
          { id: "a", mode: "preload" },
          { id: "m", mode: "available" },
          { id: "z", mode: "preload" },
        ],
      ),
    ).toEqual([
      { id: "z", mode: "preload" },
      { id: "a", mode: "preload" },
      { id: "m", mode: "available" },
    ]);
  });
});

import { describe, expect, it } from "vitest";
import { translate } from "@/lib/messages";
import type { Locale } from "@/lib/locale";
import { filterSettingsTabs, settingsTabSearch } from "@/lib/settings-search";

const TAB_IDS = ["general", "models", "policies", "context", "templates", "mcp", "skills", "packs"] as const;

// The same keys the dialog builds its nav rows from, so this suite exercises the
// shipped message text rather than a fixture of it.
function tabsFor(locale: Locale, withSession = false) {
  const t = (key: string) => translate(locale, key);
  return TAB_IDS.map((id) => ({
    id,
    ...settingsTabSearch(
      t,
      `settings.tabs.${id}.label`,
      id === "context" && withSession
        ? "settings.tabs.context.descriptionWithSession"
        : `settings.tabs.${id}.description`,
    ),
  }));
}

const matchedIds = (locale: Locale, query: string) =>
  filterSettingsTabs(tabsFor(locale), query).map((tab) => tab.id);

// Regression: the nav rows became translated (0be35ad), and the search matched
// only the displayed text — so a Chinese UI answered "models" with the empty
// state. English is a haystack in every locale.
describe("filterSettingsTabs", () => {
  it("finds the section by its Chinese label", () => {
    expect(matchedIds("zh-CN", "模型")).toEqual(["models"]);
    expect(matchedIds("zh-CN", "上下文")).toEqual(["context"]);
    expect(matchedIds("zh-CN", "技能")).toEqual(["skills"]);
    expect(matchedIds("zh-CN", "模板")).toEqual(["templates"]);
  });

  it("finds the section by its English label", () => {
    expect(matchedIds("en", "models")).toEqual(["models"]);
    expect(matchedIds("en", "context")).toEqual(["context"]);
  });

  it("still matches English queries in the Chinese UI", () => {
    expect(matchedIds("zh-CN", "models")).toEqual(["models"]);
    expect(matchedIds("zh-CN", "policies")).toEqual(["policies"]);
  });

  it("keeps every tab, in order, while the query is blank", () => {
    expect(filterSettingsTabs(tabsFor("zh-CN"), "")).toHaveLength(TAB_IDS.length);
    expect(matchedIds("zh-CN", "   ")).toEqual([...TAB_IDS]);
  });
});

describe("settingsTabSearch", () => {
  it("carries the localized text and the English baseline together", () => {
    const [models] = filterSettingsTabs(tabsFor("zh-CN"), "模型");

    expect(models.label).toBe("模型");
    expect(models.englishHaystack).toContain("Models");
  });
});

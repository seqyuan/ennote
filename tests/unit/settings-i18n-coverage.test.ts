import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { messages } from "@/lib/messages";

/**
 * Every `t("settings.…")` key a settings surface asks for has to exist.
 *
 * `translate` falls back to the key itself, so a typo does not throw — it ships
 * as the literal `settings.mcp.test` in the middle of a button. The parity test
 * cannot catch that: it compares the two dictionaries to each other, and both
 * would be missing the same key.
 */
function settingsSources(): string[] {
  const dirs = ["components/settings", "components"];
  const files: string[] = [];
  for (const dir of dirs) {
    for (const name of readdirSync(dir)) {
      if (!name.endsWith(".tsx") && !name.endsWith(".ts")) continue;
      if (dir === "components" && !name.startsWith("Settings")) continue;
      files.push(join(dir, name));
    }
  }
  for (const name of readdirSync("hooks")) {
    if (/^use(Mcp|Skill)/.test(name) && name.endsWith(".ts")) files.push(join("hooks", name));
  }
  return files;
}

describe("settings i18n coverage", () => {
  it("asks only for keys the dictionary has", () => {
    const known = new Set(Object.keys(messages.en));
    const missing: string[] = [];

    for (const file of settingsSources()) {
      const source = readFileSync(file, "utf8");
      for (const match of source.matchAll(/["`'](settings\.[a-zA-Z0-9_.]+)["`']/g)) {
        const key = match[1];
        // Template-built keys (`settings.graph.view.${view}`) arrive here as a
        // prefix; they are checked by the prefix set below instead.
        if (key.endsWith(".")) continue;
        if (!known.has(key)) missing.push(`${file}: ${key}`);
      }
    }

    expect(missing).toEqual([]);
  });

  it("covers the template-built key families", () => {
    for (const key of ["settings.graph.view.tasks", "settings.graph.view.graph", "settings.graph.view.build"]) {
      expect(Object.keys(messages.en)).toContain(key);
      expect(Object.keys(messages["zh-CN"])).toContain(key);
    }
  });

  it("leaves no settings copy in English on the Chinese side", () => {
    // A zh value identical to its English value is either a proper noun
    // (Skills, Graph, MCP) or an untranslated string. This pins the one that
    // was genuinely missed.
    const zh = messages["zh-CN"];
    const en = messages.en;
    const same = Object.keys(en).filter(key => key.startsWith("settings.") && zh[key] === en[key]);
    expect(same).not.toContain("settings.models.customRoute");
  });
});

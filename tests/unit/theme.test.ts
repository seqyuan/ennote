import { describe, expect, it } from "vitest";

import { parseThemePreference, resolveTheme } from "../../lib/theme";

describe("theme preference", () => {
  it("accepts system, light, and dark and rejects stale values", () => {
    expect(parseThemePreference("system")).toBe("system");
    expect(parseThemePreference("light")).toBe("light");
    expect(parseThemePreference("dark")).toBe("dark");
    expect(parseThemePreference("github-dark")).toBe("system");
    expect(parseThemePreference(null)).toBe("system");
  });

  it("resolves system preference from the color-scheme media query", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });
});

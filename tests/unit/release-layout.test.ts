import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const root = resolve(import.meta.dirname, "../..");

function read(path: string): string {
  return readFileSync(resolve(root, path), "utf8");
}

describe("release layout", () => {
  it("builds a static frontend without a production Next.js server", () => {
    expect(read("next.config.ts")).toMatch(/output:\s*["']export["']/);

    const pkg = JSON.parse(read("package.json")) as {
      files?: string[];
      scripts?: Record<string, string>;
    };
    expect(pkg.files).toContain("out");
    expect(pkg.files).toContain("skills");
    expect(pkg.scripts?.start).toBeUndefined();
    expect(pkg.scripts?.["build:web"]).toContain("next build");
  });

  it("ships the built-in skill-creator skill and its creation tool", () => {
    expect(existsSync(resolve(root, "skills/skill-creator/SKILL.md"))).toBe(true);
    const manifest = JSON.parse(read("skills/skill-creator/skill.json")) as { id?: string; prompt?: string };
    expect(manifest.id).toBe("skill-creator");
    expect(manifest.prompt).toBe("SKILL.md");
    expect(read("skills/skill-creator/SKILL.md")).toContain("create_skill");
    expect(read("bin/ennote.js")).toContain("ENNOTE_BUILTIN_SKILLS_DIR");
  });

  it("routes browser Worker requests through ennogate", () => {
    expect(existsSync(resolve(root, "app/api/worker/[...path]/route.ts"))).toBe(false);
    expect(read("lib/worker-api.client.ts")).toContain("/api/worker");
  });
});

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
    expect(pkg.scripts?.start).toBeUndefined();
    expect(pkg.scripts?.["build:web"]).toContain("next build");
  });

  it("routes browser Worker requests through ennogate", () => {
    expect(existsSync(resolve(root, "app/api/worker/[...path]/route.ts"))).toBe(false);
    expect(read("lib/worker-api.client.ts")).toContain("/api/worker");
  });
});

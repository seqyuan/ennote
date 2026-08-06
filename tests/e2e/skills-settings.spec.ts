import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-06T00:00:00Z";
const project = { id: "skills-project", name: "Skills lab", description: "", status: "active", createdAt: now, updatedAt: now };

const installed = [
  { name: "web-search", description: "Web search via Brave API.", filePath: "/home/u/.pi/agent/skills/web-search/SKILL.md",
    baseDir: "/home/u/.pi/agent/skills/web-search", disableModelInvocation: false,
    sourceInfo: { source: "user", scope: "user" }, skillId: "web-search", relPath: "web-search",
    install: { package: "badlogic/pi-skills@web-search", scope: "global", source: "badlogic/pi-skills",
      sourceType: "github", skillsShUrl: "https://skills.sh/badlogic/pi-skills/web-search",
      skillPath: "web-search/SKILL.md", versionHash: "079a8cb038b9166f6570a1db1f8b576efbb30b24", canCheckForUpdates: true } },
  { name: "pdf", description: "Read and create PDFs.", filePath: "/home/u/.pi/agent/skills/pdf/SKILL.md",
    baseDir: "/home/u/.pi/agent/skills/pdf", disableModelInvocation: true,
    sourceInfo: { source: "user", scope: "user" }, skillId: "pdf", relPath: "pdf",
    install: { package: "openai/skills@pdf", scope: "global", source: "openai/skills",
      sourceType: "github", skillPath: "skills/.curated/pdf/SKILL.md", versionHash: "4cffaac4541278a5b142f9773e8a83ccbabc9231", canCheckForUpdates: true } },
];

const local = [
  { name: "builtin-thing", description: "Builtin skill.", filePath: "/home/u/.ennote/skills/builtin-thing/SKILL.md",
    baseDir: "/home/u/.ennote/skills/builtin-thing", disableModelInvocation: false,
    sourceInfo: { source: "builtin", scope: "builtin" }, skillId: "builtin-thing", relPath: "builtin-thing", install: undefined },
];

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("skills tab lists installed and catalog skills with install annotations", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    if (path === "/v1/skills") return fulfill(route, { skills: [...installed, ...local], diagnostics: [], projectResourcesLoaded: false });
    return route.abort();
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Open settings" }).click();
  await page.getByRole("tab", { name: /Skills/ }).click();

  await expect(page.getByRole("heading", { name: "Skills" })).toBeVisible();
  // Installed group shows package + version + scope + skills.sh link.
  await expect(page.getByText("badlogic/pi-skills@web-search")).toBeVisible();
  await expect(page.getByText("079a8cb0")).toBeVisible();
  await expect(page.locator(".skill-row-scope", { hasText: "global" }).first()).toBeVisible();
  // Disabled flag reflected.
  await expect(page.getByRole("button", { name: "Disabled", exact: true })).toHaveCount(1);
  // Local catalog group.
  await expect(page.getByText("builtin-thing", { exact: false })).toBeVisible();
});

test("marketplace search installs a skill", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  let installBody: Record<string, unknown> | null = null;
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    if (path === "/v1/skills") return fulfill(route, { skills: [], diagnostics: [], projectResourcesLoaded: false });
    if (path === "/v1/skills/search") return fulfill(route, { results: [{ package: "acme/skills@plotly", installs: "12.5K installs", url: "https://skills.sh/acme/skills/plotly" }] });
    if (path === "/v1/skills/install") {
      installBody = JSON.parse(route.request().postData() ?? "{}");
      return fulfill(route, { success: true, output: "Installation complete" });
    }
    return route.abort();
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Open settings" }).click();
  await page.getByRole("tab", { name: /Skills/ }).click();

  await page.getByLabel("Search skills.sh").fill("plotly");
  await page.getByRole("button", { name: /Search/ }).click();
  await expect(page.getByText("acme/skills@plotly", { exact: true })).toBeVisible();
  await expect(page.getByText("12.5K installs")).toBeVisible();

  await page.getByRole("button", { name: "Install", exact: true }).click();
  await expect.poll(() => installBody).not.toBeNull();
  expect(installBody).toMatchObject({ package: "acme/skills@plotly", scope: "global" });
});

test("toggle, check update, and remove a skill", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const patched: string[] = [];
  let checkBody: Record<string, unknown> | null = null;
  let updateBody: Record<string, unknown> | null = null;
  let removed: string | null = null;
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    if (path === "/v1/skills") return fulfill(route, { skills: [installed[1]], diagnostics: [], projectResourcesLoaded: false });
    if (path.startsWith("/v1/skills/disabled/")) {
      patched.push(path);
      return route.fulfill({ status: 204 });
    }
    if (path === "/v1/skills/check") {
      checkBody = JSON.parse(route.request().postData() ?? "{}");
      return fulfill(route, { updates: [{ package: "openai/skills@pdf", scope: "global", state: "update-available",
        currentVersion: "4cffaac4541278a5b142f9773e8a83ccbabc9231", latestVersion: "newhash" }] });
    }
    if (path === "/v1/skills/update") {
      updateBody = JSON.parse(route.request().postData() ?? "{}");
      return fulfill(route, { success: true, output: "Installed 1 skill" });
    }
    if (path.startsWith("/v1/skills/remove/")) {
      removed = path;
      return route.fulfill({ status: 204 });
    }
    return route.abort();
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Open settings" }).click();
  await page.getByRole("tab", { name: /Skills/ }).click();

  // Toggle on (fixture starts disabled) → PATCH disabled/<relPath> with false.
  await page.getByRole("button", { name: "Disabled", exact: true }).click();
  await expect.poll(() => patched).toContain("/v1/skills/disabled/pdf");
  await expect(page.getByRole("button", { name: "Enabled", exact: true })).toBeVisible();

  // Check → update-available badge → Update.
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByText("Update available")).toBeVisible();
  await page.getByRole("button", { name: "Update", exact: true }).click();
  await expect.poll(() => updateBody).not.toBeNull();
  expect(updateBody).toMatchObject({ package: "openai/skills@pdf", scope: "global" });

  // Remove (confirm dialog).
  page.on("dialog", (dialog) => void dialog.accept());
  await page.locator('button[title="Remove skill"]').click();
  await expect.poll(() => removed).toBe("/v1/skills/remove/pdf");
});

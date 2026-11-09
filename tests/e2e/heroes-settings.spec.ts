import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

const now = "2026-08-08T00:00:00Z";
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-role", providerId: provider.id, modelName: "role-model", displayName: "Role Model", contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "medium"], isDefault: true, status: "active", createdAt: now, updatedAt: now };

function documentFixture() {
  return {
    schemaVersion: 1, handle: "security-reviewer", name: "Security Reviewer", description: "Independent review", positioning: "Use after trust-boundary changes.", icon: "shield-check", color: "neutral",
    model: { ref: "anthropic/role-model", thinkingEffort: "medium", fallbacks: [] }, skills: [{ id: "global/web-search", mode: "available" }],
    authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read", "grep"],
    context: { defaultMode: "room", allowedModes: ["room", "fresh"], ownExecutionContinuity: "none" },
    delegation: { admission: "approval_required", allowedCallerKinds: ["host"], allowedStrategies: ["single"], maxInvocationsPerParentRun: 1, maxConcurrentInstances: 1, budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 } },
    outputContract: "text-v1", maxLoopIterations: 8, prompt: "Review evidence independently.",
  };
}

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockHeroes(page: Page, onPatch?: (body: Record<string, unknown>) => void, onPublish?: () => void) {
  let digest = "sha256:" + "a".repeat(64);
  let document = documentFixture();
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/global-heroes" && method === "GET") return fulfill(route, [{ id: "security-reviewer", name: "Security Reviewer", path: "/home/heroes/security-reviewer/hero.md", digest }]);
    if (path === "/v1/global-heroes/security-reviewer" && method === "GET") return fulfill(route, { id: "security-reviewer", name: document.name, path: "/home/heroes/security-reviewer/hero.md", digest, document });
    if (path === "/v1/global-heroes/security-reviewer" && method === "PATCH") {
      const body = JSON.parse(route.request().postData() ?? "{}");
      onPatch?.(body);
      document = body.document;
      digest = "sha256:" + "b".repeat(64);
      return fulfill(route, { id: "security-reviewer", name: document.name, path: "/home/heroes/security-reviewer/hero.md", digest, document });
    }
    if (path === "/v1/global-heroes/security-reviewer/publish") {
      onPublish?.();
      return fulfill(route, { role: { id: "runtime-role" }, version: { id: "role-v1", version: 1 } }, 201);
    }
    return route.abort();
  });
}

test("global file Role auto-saves structured fields and publishes explicitly", async ({ page }) => {
  let patch: Record<string, unknown> | undefined;
  let published = false;
  await mockHeroes(page, (body) => { patch = body; }, () => { published = true; });
  await page.goto("/heroes");
  await expect(page.getByRole("option", { name: /Security Reviewer/ })).toBeVisible();
  await page.getByLabel("Description").fill("Independent security and privacy review");
  await page.getByLabel("Description").blur();
  await expect.poll(() => patch).toBeTruthy();
  expect(patch?.expectedDigest).toBe("sha256:" + "a".repeat(64));
  expect((patch?.document as { description: string }).description).toBe("Independent security and privacy review");
  expect(published).toBe(false);
  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await expect.poll(() => published).toBe(true);
});

test("global file Role authors the context modes delegation and reply need", async ({ page }) => {
  const patches: Array<Record<string, unknown>> = [];
  await mockHeroes(page, (body) => { patches.push(body); });
  await page.goto("/heroes");
  await expect(page.getByRole("option", { name: /Security Reviewer/ })).toBeVisible();
  await expect(page.getByLabel("Default context mode")).toHaveValue("room");
  // task_only is what lets the Hero run as a delegated child; without a UI
  // control for it a Hero created here could never be delegated to.
  await page.getByRole("checkbox", { name: "Task only" }).check();
  await expect.poll(() => patches.length).toBeGreaterThan(0);
  const document = patches.at(-1)?.document as { context: { allowedModes: string[] } };
  expect(document.context.allowedModes).toContain("task_only");
  await page.getByRole("checkbox", { name: "Reply" }).check();
  await expect.poll(() => patches.length).toBeGreaterThan(1);
  const replied = patches.at(-1)?.document as { context: { allowedModes: string[] } };
  expect(replied.context.allowedModes).toEqual(expect.arrayContaining(["reply", "task_only"]));
});

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`global Heroes editor is usable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockHeroes(page);
    await page.goto("/heroes");
    await expect(page.getByRole("heading", { name: "Heroes", level: 1 })).toBeVisible();
    await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Security Reviewer");
    await expect(page.getByLabel("Handle", { exact: true })).toHaveValue("security-reviewer");
    await expect(page.getByText("/home/heroes/security-reviewer/hero.md", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Hero prompt")).toHaveValue("Review evidence independently.");
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
    if (viewport.width < 500) {
      await page.getByTestId("heroes-page").getByRole("button", { name: "Heroes", exact: true }).click();
      await expect(page.getByRole("option", { name: /Security Reviewer/ })).toBeVisible();
    }
  });
}

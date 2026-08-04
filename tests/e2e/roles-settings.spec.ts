import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-03T00:00:00Z";
const model = { id: "model-role", providerId: "provider", modelName: "role-model", displayName: "Role Model",
  contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true,
  supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "medium"],
  isDefault: true, status: "active", createdAt: now, updatedAt: now };
const definition = {
  schemaVersion: 1, rolePrompt: "Review evidence independently.",
  modelBinding: { mode: "fixed", modelProfileId: model.id, thinkingEffort: "medium", fallbackModelProfileIds: [], overridableFields: [] },
  skills: { entries: [] }, authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read", "grep"],
  contextPolicy: { defaultMode: "room", allowedModes: ["room", "fresh"], ownExecutionContinuity: "none" },
  delegationPolicy: { admission: "approval_required", allowedCallerKinds: ["host"], allowedStrategies: ["single"],
    maxInvocationsPerParentRun: 1, maxConcurrentInstances: 1,
    budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000,
      maxCostUsdMicros: 100000, maxWallTimeMs: 120000 } },
  outputContract: "text-v1", maxLoopIterations: 8,
};
const role = { id: "security-role", handle: "security-reviewer", name: "Security Reviewer",
  description: "Independent review", positioning: "Use after trust-boundary changes.", icon: "shield-check", color: "#b91c1c",
  scope: "global", status: "active", draft: definition, draftRevision: 0, currentVersionId: "security-v1", currentVersion: 1,
  delegationEnabled: true, delegationRevocationEpoch: 0, createdAt: now, updatedAt: now };

function fulfill(route: Route, data: unknown) {
  return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockRoles(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [role], nextCursor: "" });
    if (path === `/v1/roles/${role.id}`) return fulfill(route, role);
    if (path === `/v1/roles/${role.id}/versions`) return fulfill(route, [{ id: "security-v1", roleId: role.id,
      version: 1, definition, configDigest: `sha256:${"a".repeat(64)}`, status: "published", createdAt: now }]);
    return route.abort();
  });
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`Roles editor is usable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockRoles(page);
    await page.goto("/");
    await page.getByRole("button", { name: "Open settings" }).click();
    await page.getByRole("tab", { name: /Roles/ }).click();
    await expect(page.getByRole("heading", { name: "Roles" })).toBeVisible();
    await page.getByRole("button", { name: /Security Reviewer/ }).click();
    await expect(page.getByRole("textbox", { name: "Handle" })).toHaveValue("security-reviewer");
    await expect(page.getByRole("textbox", { name: "Role prompt" })).toHaveValue("Review evidence independently.");
    await expect(page.getByText("Published v1")).toBeVisible();
    await page.getByTitle("Version history").click();
    await expect(page.locator(".role-version-strip").getByText("v1", { exact: false })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow).toBe(false);
  });
}

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
    if (path === `/v1/roles/${role.id}/draft`) {
      const body = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
      draftBody = body;
      return fulfill(route, { ...role, draftRevision: 1, draft: body.definition });
    }
    if (path === `/v1/roles/${role.id}/validate`) return fulfill(route, { valid: true, diagnostics: [] });
    if (path === `/v1/roles/${role.id}/publish`) {
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ data: { ...role, draftRevision: 0 } }) });
    }
    if (path === "/v1/skills") return fulfill(route, { skills: [catalogSkill], diagnostics: [], projectResourcesLoaded: false });
    return route.abort();
  });
}

const catalogSkill = { name: "web-search", description: "Web search.", filePath: "/s/web-search/SKILL.md",
  baseDir: "/s/web-search", disableModelInvocation: false, sourceInfo: { source: "user", scope: "user" },
  skillId: "web-search", relPath: "web-search", install: undefined };
let draftBody: Record<string, unknown> | null = null;


// Skill binding writes RoleDefinition.skills.entries and survives publish.
test("role editor binds catalog skills with a mode and publishes them", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await mockRoles(page);
  await page.goto("/roles");
  await page.getByRole("button", { name: /Security Reviewer/ }).click();

  // Search narrows the catalog; bind with preload mode.
  await expect(page.getByText("web-search")).toBeVisible();
  await page.getByPlaceholder("Search skills…").fill("web");
  await page.locator(".role-skill-row").getByRole("checkbox").check();
  await page.locator(".role-skill-mode").selectOption("preload");
  await expect(page.getByText("1 bound")).toBeVisible();

  // Save draft carries skills.entries.
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => draftBody).not.toBeNull();
  const definition = (draftBody as Record<string, unknown>)["definition"] as Record<string, unknown>;
  const skills = (definition as Record<string, unknown>)["skills"] as Record<string, unknown>;
  expect((skills as Record<string, unknown>)["entries"]).toMatchObject([{ skillId: "web-search", mode: "preload" }]);
});

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`Roles editor is usable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockRoles(page);
    await page.goto("/roles");
    await expect(page.getByRole("heading", { name: "Roles", level: 1 })).toBeVisible();
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

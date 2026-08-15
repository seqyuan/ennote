import { expect, test, type Route } from "@playwright/test";

const now = "2026-08-08T00:00:00Z";
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "model", providerId: provider.id, modelName: "sonnet", displayName: "Sonnet", contextWindow: 32000, maxOutputTokens: 4096, supportsVision: true, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default"], isDefault: true, status: "active", createdAt: now, updatedAt: now };
const baseDocument = {
  schemaVersion: 1, handle: "reviewer", name: "Reviewer", description: "Original description", positioning: "", icon: "bot", color: "neutral",
  model: { ref: "anthropic/sonnet", thinkingEffort: "default", fallbacks: [] }, skills: [], authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read"],
  context: { defaultMode: "room", allowedModes: ["room", "fresh"], ownExecutionContinuity: "none" },
  delegation: { admission: "approval_required", allowedCallerKinds: ["host"], allowedStrategies: ["single"], maxInvocationsPerParentRun: 1, maxConcurrentInstances: 1, budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 } },
  outputContract: "text-v1", maxLoopIterations: 8, prompt: "Review independently.",
};

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("external Role file changes fail closed and expose Reload and Diff", async ({ page }) => {
  let diskChanged = false;
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/global-roles") return fulfill(route, [{ id: "reviewer", name: "Reviewer", path: "/home/roles/reviewer/role.md", digest: "sha256:old" }]);
    if (path === "/v1/global-roles/reviewer" && route.request().method() === "GET") {
      const document = diskChanged ? { ...baseDocument, description: "Externally edited description" } : baseDocument;
      return fulfill(route, { id: "reviewer", name: "Reviewer", path: "/home/roles/reviewer/role.md", digest: diskChanged ? "sha256:new" : "sha256:old", document });
    }
    if (path === "/v1/global-roles/reviewer" && route.request().method() === "PATCH") {
      expect(route.request().postDataJSON().expectedDigest).toBe("sha256:old");
      diskChanged = true;
      return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: { code: "source_conflict", message: "Role file changed on disk" } }) });
    }
    return route.abort();
  });

  await page.goto("/roles");
  const description = page.getByLabel("Description");
  await description.fill("My local edit");
  await description.blur();
  await expect(page.getByText("File changed on disk", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Publish", exact: true })).toBeDisabled();

  await page.getByRole("button", { name: "Diff", exact: true }).click();
  const diff = page.getByRole("dialog", { name: "Role file diff" });
  await expect(diff).toContainText("My local edit");
  await expect(diff).toContainText("Externally edited description");
  await diff.getByRole("button", { name: "Close" }).click();

  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(description).toHaveValue("Externally edited description");
  await expect(page.getByText("File changed on disk", { exact: true })).toHaveCount(0);
});

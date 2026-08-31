import { expect, test, type Route } from "@playwright/test";
import { tryFulfillBlankSessionCreate } from "./harness";

const now = "2026-08-08T00:00:00Z";
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "model", providerId: provider.id, modelName: "sonnet", displayName: "Sonnet", contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "high"], isDefault: true, status: "active", createdAt: now, updatedAt: now };

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("Graph-local Role reference is isolated and Role-backed Tasks cannot override runtime fields", async ({ page }) => {
  let patchedTask: Record<string, unknown> | undefined;
  const roleTask = { name: "Review", role: "local/reviewer", goal: "Review outputs." };
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs") return fulfill(route, [{ id: "review", name: "Review", path: "/home/graphs/review/graph.yaml", digest: "sha256:a", latestVersion: 0 }]);
    if (path === "/v1/graphs/review" && route.request().method() === "GET") return fulfill(route, { id: "review", name: "Review", path: "/home/graphs/review/graph.yaml", digest: "sha256:a", latestVersion: 0, document: { schemaVersion: 1, id: "review", name: "Review", description: "", tasks: { review: roleTask }, graph: { review: [] } } });
    if (path === "/v1/graphs/review" && route.request().method() === "PATCH") {
      const body = route.request().postDataJSON();
      patchedTask = body.task.value;
      return fulfill(route, { id: "review", name: "Review", path: "/home/graphs/review/graph.yaml", digest: "sha256:b", latestVersion: 0, document: { schemaVersion: 1, id: "review", name: "Review", description: "", tasks: { review: patchedTask }, graph: { review: [] } } });
    }
    if (path === "/v1/graphs/review/versions") return fulfill(route, []);
    if (path === "/v1/graphs/review/builder") return fulfill(route, { graphId: "review", modelProfileId: model.id, messages: [] });
    return route.abort();
  });

  await page.goto("/graphs");
  const task = page.locator('[data-task-id="review"]');
  await task.locator(".graph-task-summary").click();
  await expect(task.getByRole("button", { name: "Use a Role" })).toHaveAttribute("aria-pressed", "true");
  await expect(task.getByLabel("Role")).toHaveValue("local/reviewer");
  await expect(task.getByLabel("Model")).toHaveCount(0);
  await expect(task.getByLabel("Thinking")).toHaveCount(0);
  await expect(task.getByLabel("Skills")).toHaveCount(0);

  await task.getByRole("button", { name: "Inline configuration" }).click();
  await expect.poll(() => patchedTask).toBeTruthy();
  expect(patchedTask).toMatchObject({ model: "anthropic/sonnet", thinking: "default", skills: [] });
  expect(patchedTask).not.toHaveProperty("role");
});

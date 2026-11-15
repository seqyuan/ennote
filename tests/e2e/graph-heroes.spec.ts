import { expect, test, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

const now = "2026-08-08T00:00:00Z";
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "model", providerId: provider.id, modelName: "sonnet", displayName: "Sonnet", contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "high"], isDefault: true, status: "active", createdAt: now, updatedAt: now };

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

function graphDetail(definition: Record<string, unknown>, digest: string) {
  return { id: "review", name: "Review", path: "/home/graphs/review/graph.yaml", digest, latestVersion: 0, definition };
}

// A Task carries a Hero reference and nothing else: the runtime fields (model,
// thinking, skills) belong to the Hero version a Run freezes, so the editor has
// no inline form for them. Saving another field must keep the reference, and the
// request must never grow an inline override the Worker would have to ignore.
test("Graph Task keeps its Hero reference and exposes no inline runtime fields", async ({ page }) => {
  let patchedTask: Record<string, unknown> | undefined;
  const heroTask = { hero: "local/reviewer", goal: "Review outputs." };
  const definition = {
    schemaVersion: 1, id: "review", name: "Review", description: "",
    budget: { maxTotalTokens: 1000 },
    tasks: { review: heroTask },
  };
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs") return fulfill(route, [{ id: "review", name: "Review", path: "/home/graphs/review/graph.yaml", digest: "sha256:a", latestVersion: 0 }]);
    if (path === "/v1/graphs/review" && route.request().method() === "GET") {
      return fulfill(route, graphDetail(definition, "sha256:a"));
    }
    if (path === "/v1/graphs/review" && route.request().method() === "PATCH") {
      const body = route.request().postDataJSON();
      patchedTask = body.task.value;
      const tasks = { review: { ...heroTask, ...body.task.value } };
      return fulfill(route, graphDetail({ ...definition, tasks }, "sha256:b"));
    }
    if (path === "/v1/graphs/review/versions") return fulfill(route, []);
    if (path === "/v1/graphs/review/builder") return fulfill(route, { graphId: "review", modelProfileId: model.id, messages: [] });
    return route.abort();
  });

  await page.goto("/graphs");
  const task = page.locator('[data-task-id="review"]');
  await expect(task.locator(".graph-task-summary")).toContainText("local/reviewer");
  await task.locator(".graph-task-summary").click();

  const hero = task.getByLabel("Hero");
  await expect(hero).toHaveValue("local/reviewer");
  // No inline runtime configuration exists for a Task: those fields are the
  // Hero's, frozen per Run.
  await expect(task.getByLabel("Model")).toHaveCount(0);
  await expect(task.getByLabel("Thinking")).toHaveCount(0);

  const goal = task.getByLabel("Goal");
  await goal.fill("Review the QC outputs.");
  await goal.blur();

  await expect.poll(() => patchedTask).toBeTruthy();
  expect(patchedTask).toMatchObject({ hero: "local/reviewer", goal: "Review the QC outputs." });
  expect(patchedTask).not.toHaveProperty("model");
  expect(patchedTask).not.toHaveProperty("thinking");
  expect(patchedTask).not.toHaveProperty("skills");
});

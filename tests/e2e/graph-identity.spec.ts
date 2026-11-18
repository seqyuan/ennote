import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

// The header's job is to keep four facts apart. The bug it replaces printed
// "Saved v7" — a save state wearing a version number — so a reader could not tell
// whether the edits on screen were in v7, or what any Project would actually run.

const now = "2026-08-07T00:00:00Z";
const provider = { id: "provider-1", name: "Local", type: "openai", baseUrl: "http://127.0.0.1:1", apiKeySet: true, status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-1", providerId: provider.id, modelName: "gpt-test", displayName: "Test model", contextTokens: 8000, maxOutputTokens: 1000, status: "active", createdAt: now, updatedAt: now };

const draftDigest = "sha256:" + "a".repeat(64);
const publishedDigest = "sha256:" + "b".repeat(64);

function graphDefinition(goal: string) {
  return {
    schemaVersion: 1, id: "rna-seq", name: "RNA-seq", budget: { maxTotalTokens: 200000 },
    tasks: { align_1: { hero: "global/aligner", goal } },
  };
}

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockIdentity(page: Page, bindings: { projectId: string; revision: string; enabled: boolean }[]) {
  const state = { patches: 0, definition: graphDefinition("Align reads"), digest: draftDigest };
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs" && method === "GET") {
      return fulfill(route, [{ id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 7 }]);
    }
    if (path === "/v1/graphs/rna-seq" && method === "GET") {
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 7, definition: state.definition });
    }
    if (path === "/v1/graphs/rna-seq/versions") {
      return fulfill(route, [{ id: "graph-v7", resourceId: "rna-seq", sourceKind: "graph", version: 7, digest: publishedDigest, publishedAt: now }]);
    }
    if (path === "/v1/graphs/rna-seq/bindings") return fulfill(route, bindings);
    if (path === "/v1/graphs/rna-seq" && method === "PATCH") {
      state.patches += 1;
      const body = JSON.parse(route.request().postData() ?? "{}") as { task?: { id: string; value: Record<string, unknown> } };
      if (body.task) state.definition = { ...state.definition, tasks: { ...state.definition.tasks, [body.task.id]: body.task.value } };
      state.digest = "sha256:" + "c".repeat(64);
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 7, definition: state.definition });
    }
    if (path === "/v1/graphs/rna-seq/builder") return fulfill(route, { graphId: "rna-seq", modelProfileId: model.id, messages: [] });
    return route.abort();
  });
  return state;
}

async function openGraphs(page: Page) {
  const secret = process.env.ENNOTE_E2E_PASSWORD ?? "preview1234";
  await page.goto("/");
  const headers = { Origin: new URL(page.url()).origin };
  const statusResponse = await page.request.get("/api/auth/status");
  if (statusResponse.ok() && statusResponse.headers()["content-type"]?.includes("application/json")) {
    const status = await statusResponse.json() as { requiresPassword: boolean; authenticated: boolean };
    if (!status.requiresPassword) await page.request.post("/api/auth/setup", { data: { password: secret }, headers });
    if (!status.authenticated) {
      const login = await page.request.post("/api/auth/login", { data: { password: secret }, headers });
      expect(login.ok()).toBe(true);
    }
  }
  await page.goto("/graphs");
  await expect(page.locator(".graph-identity")).toBeVisible();
}

test("the header separates the draft from the published revision", async ({ page }) => {
  await mockIdentity(page, [{ projectId: "proj-a", revision: "v000007", enabled: true }]);
  await openGraphs(page);

  const identity = page.locator(".graph-identity");
  // The draft digest is not the published one, so this draft is not what runs.
  await expect(identity).toContainText("Not published");
  await expect(identity.getByText("v7", { exact: true })).toBeVisible();
  await expect(identity).toContainText("proj-a");
  await expect(identity).toContainText("v000007");
  // The header names no Run: a Graph is global and its Runs live in each Session,
  // so "which assembly ran" is answered on the Run, not here.
  await expect(identity).not.toContainText("Last run");
});

test("an edit keeps the draft unpublished until it is published", async ({ page }) => {
  const state = await mockIdentity(page, [{ projectId: "proj-a", revision: "v000007", enabled: true }]);
  await openGraphs(page);

  const task = page.locator('[data-task-id="align_1"]');
  await task.locator(".graph-task-summary").click();
  await task.getByLabel("Goal").fill("Align with a new aligner");

  await expect.poll(() => state.patches).toBe(1);
  await expect(page.locator(".graph-identity")).toContainText("Not published");
  // The published revision is unchanged by a save; only a publish moves it.
  await expect(page.locator(".graph-identity").getByText("v7", { exact: true })).toBeVisible();
});

test("a bound-but-disabled Project and an unpinned binding are not versions", async ({ page }) => {
  await mockIdentity(page, [
    { projectId: "proj-off", revision: "v000007", enabled: false },
    { projectId: "proj-open", revision: "", enabled: true },
  ]);
  await openGraphs(page);

  const identity = page.locator(".graph-identity");
  // Disabled: bound, but the header must not claim it would run.
  await expect(identity).toContainText("proj-open");
  await expect(identity).toContainText("not pinned");
  await expect(identity).not.toContainText("proj-off");
});

test("a Graph bound to nothing says so instead of showing a blank", async ({ page }) => {
  await mockIdentity(page, []);
  await openGraphs(page);
  await expect(page.locator(".graph-identity")).toContainText("Not bound to any Project");
});

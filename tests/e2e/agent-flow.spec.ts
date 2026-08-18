import { expect, test, type Page, type Route } from "@playwright/test";

test.describe.configure({ mode: "serial" });

const now = "2026-08-08T00:00:00Z";
const provider = { id: "provider-1", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test/v1", credentialConfigured: true, status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-1", providerId: provider.id, modelName: "claude-sonnet-4", displayName: "Claude Sonnet", contextWindow: 200000, maxOutputTokens: 8000, inputCostUsdMicrosPerMillion: 0, outputCostUsdMicrosPerMillion: 0, supportsVision: true, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "low", "medium", "high"], isDefault: true, status: "active", createdAt: now, updatedAt: now };

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

interface FixtureTask {
  name: string;
  goal: string;
  role?: string;
  model?: string;
  thinking?: string;
  skills?: string[];
}

interface FixtureGraphDocument {
  schemaVersion: number;
  id: string;
  name: string;
  description: string;
  tasks: Record<string, FixtureTask>;
  graph: Record<string, string[]>;
}

function graphDocument(): FixtureGraphDocument {
  return {
    schemaVersion: 1, id: "rna-seq", name: "RNA-seq", description: "",
    tasks: {
      prepare_reference: { name: "Prepare reference", role: "local/reference-preparer", goal: "Prepare the genome index." },
      align_1: { name: "Align batch 1", model: "anthropic/claude-sonnet-4", thinking: "high", skills: ["local/alignment", "global/report-writing"], goal: "Align paired-end reads." },
    },
    graph: { prepare_reference: [], align_1: ["prepare_reference"] },
  };
}

async function openGraphs(page: Page) {
  const secret = process.env.ENNOTE_E2E_PASSWORD ?? "preview1234";
  await page.goto("/");
  const headers = { Origin: new URL(page.url()).origin };
  const statusResponse = await page.request.get("/api/auth/status");
  if (statusResponse.ok() && statusResponse.headers()["content-type"]?.includes("application/json")) {
    const status = await statusResponse.json() as { requiresPassword: boolean; authenticated: boolean };
    if (!status.requiresPassword) {
      await page.request.post("/api/auth/setup", { data: { password: secret }, headers });
    }
    if (!status.authenticated) {
      const login = await page.request.post("/api/auth/login", { data: { password: secret }, headers });
      expect(login.ok()).toBe(true);
    }
  }
  await page.goto("/graphs");
}

async function mockGraphAuthoring(page: Page) {
  let digest = "sha256:" + "a".repeat(64);
  let document = graphDocument();
  let builderThread: Record<string, unknown> = { graphId: "rna-seq", modelProfileId: model.id, messages: [] };
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs" && method === "GET") return fulfill(route, [{ id: "rna-seq", name: "RNA-seq", path: "/home/graphs/rna-seq/graph.yaml", digest, latestVersion: 1 }]);
    if (path === "/v1/graphs" && method === "POST") return fulfill(route, { id: "new-graph", name: "New Graph", path: "/home/graphs/new-graph/graph.yaml", digest, latestVersion: 0, document: { schemaVersion: 1, id: "new-graph", name: "New Graph", tasks: {}, graph: {} } }, 201);
    if (path === "/v1/graphs/rna-seq" && method === "GET") return fulfill(route, { id: "rna-seq", name: document.name, path: "/home/graphs/rna-seq/graph.yaml", digest, latestVersion: 1, document });
    if (path === "/v1/graphs/rna-seq" && method === "PATCH") {
      const body = JSON.parse(route.request().postData() ?? "{}");
      if (body.task) {
        if (body.task.value) {
          document = { ...document, tasks: { ...document.tasks, [body.task.id]: body.task.value }, graph: { ...document.graph, [body.task.id]: document.graph[body.task.id] ?? [] } };
        } else {
          const tasks = { ...document.tasks };
          const graph = { ...document.graph };
          delete tasks[body.task.id];
          delete graph[body.task.id];
          document = { ...document, tasks, graph };
        }
      }
      if (body.dependencies) document = { ...document, graph: { ...document.graph, [body.dependencies.taskId]: body.dependencies.depends } };
      digest = "sha256:" + "b".repeat(64);
      return fulfill(route, { id: "rna-seq", name: document.name, path: "/home/graphs/rna-seq/graph.yaml", digest, latestVersion: 1, document });
    }
    if (path === "/v1/graphs/rna-seq/versions") return fulfill(route, [{ id: "graph-v1", profileId: "profile-1", version: 1, configDigest: "c".repeat(64), definition: {}, publishedAt: now }]);
    if (path === "/v1/graphs/rna-seq/publish") return fulfill(route, { id: "graph-v2", profileId: "profile-1", version: 2, configDigest: "d".repeat(64), definition: {}, publishedAt: now }, 201);
    if (path === "/v1/graphs/rna-seq/builder" && method === "GET") return fulfill(route, builderThread);
    if (path === "/v1/graphs/rna-seq/builder/messages") {
      builderThread = { graphId: "rna-seq", modelProfileId: model.id, messages: [
        { id: "m1", graphId: "rna-seq", ordinal: 1, role: "user", content: "Add QC", createdAt: now },
        { id: "m2", graphId: "rna-seq", ordinal: 2, role: "assistant", content: "Add a QC Task after alignment.", createdAt: now },
      ], proposal: { id: "proposal-1", graphId: "rna-seq", baseDigest: digest, summary: "Add a QC Task after alignment.", status: "pending", diagnostics: [], createdAt: now, operations: [{ kind: "upsert_task", taskId: "qc" }, { kind: "set_dependencies", taskId: "qc", depends: ["align_1"] }] } };
      return fulfill(route, builderThread, 201);
    }
    if (path === "/v1/graphs/rna-seq/builder/proposals/proposal-1/apply") {
      document = { ...document, tasks: { ...document.tasks, qc: { name: "QC", model: "anthropic/claude-sonnet-4", thinking: "default", skills: [], goal: "Review alignment quality." } }, graph: { ...document.graph, qc: ["align_1"] } };
      builderThread = { ...builderThread, proposal: undefined };
      return fulfill(route, { id: "rna-seq", name: document.name, path: "/home/graphs/rna-seq/graph.yaml", digest: "sha256:" + "e".repeat(64), latestVersion: 1, document });
    }
    return route.abort();
  });
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`Task-first global Graph editor at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockGraphAuthoring(page);
    await openGraphs(page);

    await expect(page.getByRole("tab", { name: "RNA-seq" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Add Graph" })).toBeVisible();
    // The sidebar now stays mounted next to the settings view, so scope the
    // no-empty-hero check to the editor region (the sidebar's project
    // selector legitimately shows its empty state when no project is set).
    await expect(page.locator(".settings-view").getByText("Select project", { exact: false })).toHaveCount(0);
    const task = page.locator('[data-task-id="align_1"]');
    await expect(task.locator(".graph-task-form")).toHaveCount(0);
    await task.locator(".graph-task-summary").click();
    await expect(task.getByRole("button", { name: "Inline configuration" })).toHaveAttribute("aria-pressed", "true");
    await expect(task.getByLabel("Model")).toHaveValue("anthropic/claude-sonnet-4");
    await expect(task.getByLabel("Thinking")).toHaveValue("high");

    if (viewport.width < 500) {
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
      // Mobile: the settings view's menu button opens the app-shell sidebar
      // drawer (same shell as chat, labeled "Navigation").
      await page.getByRole("button", { name: "Open navigation" }).click();
      await expect(page.getByRole("dialog", { name: "Navigation" })).toBeVisible();
      await page.locator(".sidebar-close-nav").click();
      await page.getByRole("tab", { name: "graph", exact: true }).click();
    }
    await expect(page.getByText("Level 1", { exact: true })).toBeVisible();
    await expect(page.getByText("Level 2", { exact: true })).toBeVisible();
  });
}

test("Graph Builder persists a proposal and applies it explicitly", async ({ page }) => {
  await mockGraphAuthoring(page);
  await openGraphs(page);
  await page.getByLabel("Graph Builder instruction").fill("Add QC");
  await page.getByRole("button", { name: "Send Builder instruction" }).click();
  await expect(page.getByText("Add a QC Task after alignment.", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("2 changes", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Apply proposal" }).click();
  await expect(page.locator('[data-task-id="qc"]')).toBeVisible();
});

test("Graph publication is explicit and updates the visible immutable version", async ({ page }) => {
  await mockGraphAuthoring(page);
  await openGraphs(page);
  await expect(page.getByText("v1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await expect(page.getByText("v2", { exact: true })).toBeVisible();
});

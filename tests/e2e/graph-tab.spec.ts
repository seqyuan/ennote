import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-07T00:00:00Z";
const later = "2026-08-07T00:10:00Z";
const project = { id: "graph-proj", name: "Graphs", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "graph-sess", projectId: project.id, title: "Graph session", status: "active",
  activeLeafMessageId: "m1", createdAt: now, updatedAt: now };
const olderRun = { runId: "run-old", sessionId: session.id, projectId: project.id, flowVersionId: "v1",
  manifestDigest: "a".repeat(64), state: "completed", totalTokensUsed: 100, inputs: { name: "Old flow" },
  createdAt: now, updatedAt: now };
const newerRun = { runId: "run-new", sessionId: session.id, projectId: project.id, flowVersionId: "v1",
  manifestDigest: "b".repeat(64), state: "running", totalTokensUsed: 40, inputs: { name: "New flow" },
  createdAt: later, updatedAt: later };
const otherSessionRun = { runId: "run-other", sessionId: "other-sess", projectId: project.id, flowVersionId: "v1",
  manifestDigest: "c".repeat(64), state: "completed", totalTokensUsed: 50, inputs: { name: "Other" },
  createdAt: later, updatedAt: later };
const nodes = [
  { runId: newerRun.runId, taskIndex: 0, handle: "producer", terminalState: "completed", goalText: "produce", createdAt: now },
  { runId: newerRun.runId, taskIndex: 1, handle: "reviewer", terminalState: "running", goalText: "review", createdAt: now },
];

function fulfill(route: Route, data: unknown) {
  return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockGraphTab(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/projects/${project.id}/agent-flows/runs`) {
      // Returns ALL project runs; the panel filters by the current session.
      return fulfill(route, [otherSessionRun, newerRun, olderRun]);
    }
    if (path === `/v1/projects/${project.id}/agent-flows/runs/run-new`) {
      return fulfill(route, { run: newerRun, nodes, flowVersion: 1 });
    }
    if (path === `/v1/projects/${project.id}/agent-flows/runs/run-old`) {
      return fulfill(route, { run: olderRun, nodes: [], flowVersion: 1 });
    }
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: null });
    return route.abort();
  });
}

async function openGraphTab(page: Page) {
  await mockGraphTab(page);
  await page.goto("/");
  await page.getByTitle("Select project").click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Graphs" }).click();
}

test("Graphs channel lists current-session runs, newest first, newest expanded", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openGraphTab(page);

  // Both session runs are listed; the other-session run is filtered out.
  await expect(page.getByText("New flow")).toBeVisible();
  await expect(page.getByText("Old flow")).toBeVisible();
  await expect(page.getByText("Other", { exact: true })).not.toBeVisible();

  // The newest run is expanded by default: its task checkpoints are visible.
  await expect(page.getByText("producer")).toBeVisible();
  await expect(page.getByText("reviewer")).toBeVisible();

  // Collapsing the newest run hides its tasks.
  await page.getByRole("button", { name: /New flow/ }).click();
  await expect(page.getByText("producer")).not.toBeVisible();
});

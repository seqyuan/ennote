import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-07T00:00:00Z";
const project = { id: "flow-role-proj", name: "FlowRoles", description: "", status: "active", createdAt: now, updatedAt: now };
const model = { id: "model", providerId: "provider", modelName: "model", displayName: "Default Model",
  contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true,
  supportsThinking: false, thinkingDialect: "none", supportedThinkingEfforts: ["default"],
  isDefault: true, status: "active", createdAt: now, updatedAt: now };
const profile = { id: "flow-1", name: "Go Review", slug: "go-review", sourceKind: "managed", projectScope: null,
  sourceLocator: "", lifecycleStatus: "active", latestVersion: 1, draftRevision: 0, createdAt: now, updatedAt: now };
const sharedRole = { id: "shared-role", handle: "reviewer", name: "Reviewer", description: "",
  positioning: "", icon: "bot", color: "neutral", scope: "global", status: "active",
  currentVersionId: "rv1", currentVersion: 1, updatedAt: now };

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockFlowRoles(page: Page) {
  const flowRoles: Array<Record<string, unknown>> = [];
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles" && route.request().method() === "POST") {
      const body = JSON.parse(route.request().postData() ?? "{}");
      const created = { id: `flow-role-${flowRoles.length + 1}`, handle: body.handle, name: body.name,
        description: "", positioning: "", icon: "bot", color: "#2563eb", scope: "flow", flowId: body.flowId,
        status: "active", draft: body.definition, draftRevision: 0, delegationEnabled: true,
        delegationRevocationEpoch: 0, createdAt: now, updatedAt: now };
      flowRoles.push(created);
      return fulfill(route, created, 201);
    }
    if (path === "/v1/roles") {
      const params = new URL(route.request().url()).searchParams;
      if (params.get("scope") === "flow") return fulfill(route, { items: flowRoles, nextCursor: "" });
      return fulfill(route, { items: [sharedRole], nextCursor: "" });
    }
    if (path === "/v1/agent-flows") return fulfill(route, [profile]);
    if (path === "/v1/agent-flows/flow-1") return fulfill(route, profile);
    if (path === `/v1/agent-flows/flow-1/draft`) {
      const body = JSON.parse(route.request().postData() ?? "{}");
      profile.draftRevision = (body.expectedRevision ?? 0) + 1;
      return fulfill(route, profile);
    }
    if (path === "/v1/agent-flows/flow-1/validate") return fulfill(route, { valid: true, diagnostics: [] });
    if (path === "/v1/agent-flows/flow-1/versions") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/agent-flows/candidates`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/agent-flows/bindings`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/agent-flows/runs`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/agent-flows/check-approvals`) return fulfill(route, []);
    if (path.match(/^\/v1\/roles\/flow-role-\d+$/)) {
      const id = path.split("/").pop()!;
      const role = flowRoles.find((r) => r.id === id);
      return fulfill(route, role);
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: {} }) });
  });
}

async function openGraph(page: Page) {
  await mockFlowRoles(page);
  await page.goto("/graphs");
  await page.getByRole("button", { name: /Select project/ }).first().click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  // Open the flow editor.
  await page.getByRole("button", { name: "Edit", exact: true }).first().click();
  await expect(page.getByText("Graph-local Roles", { exact: true })).toBeVisible();
}

test("graph-local role is created and offered first in the task role picker", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openGraph(page);

  // Graph-local Roles section is visible after selecting the graph.
  await expect(page.getByText("Graph-local Roles", { exact: true })).toBeVisible();

  // Create a graph-local role.
  await page.getByRole("button", { name: "New role" }).click();
  await page.getByPlaceholder("graph-writer").fill("graph-writer");
  await page.getByPlaceholder("Graph Writer").fill("Graph Writer");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  // The role appears in the Graph-local Roles list.
  await expect(page.getByRole("button", { name: /graph-writer Graph Writer/ })).toBeVisible();

  // The task role picker offers the graph-local role first (bare handle).
  await page.getByRole("button", { name: "+ Add task" }).click();
  const roleSelect = page.getByLabel(/Role \(published/).first();
  // The graph-local role appears as a bare-handle option (no @version).
  await roleSelect.selectOption("graph-writer");
  await expect(roleSelect).toHaveValue("graph-writer");
});

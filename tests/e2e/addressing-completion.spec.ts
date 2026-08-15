import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-06T00:00:00Z";
const project = { id: "addr-proj", name: "Addressing", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "addr-sess", projectId: project.id, title: "Completion", status: "active", activeLeafMessageId: "m1", createdAt: now, updatedAt: now };
const policies = ["discuss", "ask", "auto"].map((mode) => ({ id: `builtin-tool-${mode}-v1`, name: mode,
  kind: "tool", version: 1, config: { mode }, status: "active", createdAt: now, updatedAt: now }));

const role = { id: "reviewer-role", handle: "security-reviewer", name: "Security Reviewer",
  description: "Independent review", positioning: "", icon: "shield", color: "#b91c1c",
  scope: "global", projectId: null, status: "active", currentVersionId: "rv1", currentVersion: 1, updatedAt: now };

const graphs = [
  { id: "go-review", name: "Go Review", path: "/home/graphs/go-review/graph.yaml", digest: `sha256:${"a".repeat(64)}`, latestVersion: 2 },
];

function fulfill(route: Route, data: unknown) {
  return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockBackend(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: null });
    if (path === `/v1/projects/${project.id}/prompt-templates`) return fulfill(route, { templates: [], diagnostics: [] });
    if (path === "/v1/roles" && method === "GET") return fulfill(route, { items: [role], nextCursor: "" });
    if (path === "/v1/graphs" && method === "GET") return fulfill(route, graphs);
    return route.abort();
  });
}

async function openSession(page: Page) {
  await mockBackend(page);
  await page.goto("/");
  await page.getByTitle("Select project").first().click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
}

test("typing @role completes a role target and clears the token", async ({ page }) => {
  await openSession(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".prompt-command-menu");

  await textarea.fill("@role:sec");
  await expect(panel).toBeVisible();
  await expect(panel.getByText("@role:security-reviewer")).toBeVisible();

  await panel.getByText("@role:security-reviewer").click();
  // The token is consumed and the role target is selected instead.
  await expect(textarea).toHaveValue("");
  await expect(panel).not.toBeVisible();
  // The config panel trigger now shows the selected role handle.
  await page.getByRole("button", { name: "Configure run", exact: true }).click();
  await expect(page.getByText("@security-reviewer", { exact: true })).toBeVisible();
});

test("typing @graph completes a graph invocation with version", async ({ page }) => {
  await openSession(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".prompt-command-menu");

  await textarea.fill("@graph:go-");
  await expect(panel).toBeVisible();
  await expect(panel.getByText("@graph:go-review@2")).toBeVisible();

  await panel.getByText("@graph:go-review@2").click();
  // The invocation token is inserted; the existing submit gate parses it.
  await expect(textarea).toHaveValue("@graph:go-review@2 ");
  await expect(panel).not.toBeVisible();
});

test("@ addressing does not open when no match exists", async ({ page }) => {
  await openSession(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".prompt-command-menu");

  await textarea.fill("@role:zzz");
  await expect(panel).not.toBeVisible();
  await textarea.fill("@graph:nope");
  await expect(panel).not.toBeVisible();
});

import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillHarnessRoutes } from "./harness";

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

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockBackend(page: Page, onInvocation?: (body: Record<string, unknown>) => void) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: null });
    if (path === `/v1/projects/${project.id}/prompt-templates`) return fulfill(route, { templates: [], diagnostics: [] });
    if (path === "/v1/global-heroes" && method === "GET") {
      return fulfill(route, [{ id: role.id, name: role.name, path: "/home/heroes/security-reviewer/role.md", digest: `sha256:${"a".repeat(64)}` }]);
    }
    if (path === `/v1/global-heroes/${role.id}` && method === "GET") {
      return fulfill(route, {
        id: role.id, name: role.name, path: "/home/heroes/security-reviewer/role.md", digest: `sha256:${"a".repeat(64)}`,
        document: { handle: role.handle, name: role.name, description: role.description, positioning: role.positioning, icon: role.icon, color: role.color },
      });
    }
    if (path === `/v1/global-heroes/${role.id}/versions`) return fulfill(route, [{ version: 1, publishedAt: now }]);
    if (path === "/v1/graphs" && method === "GET") return fulfill(route, graphs);
    if (path === `/v1/sessions/${session.id}/invocations` && method === "POST") {
      onInvocation?.(JSON.parse(route.request().postData() ?? "{}"));
      return fulfill(route, {
        turnId: "turn-1", userMessageId: "m-1", existing: false,
        run: { id: "run-1", turnId: "turn-1", sessionId: session.id, runKind: "agent", attempt: 1,
          status: "queued", commitFormatVersion: 2, executionDepth: 0, publishMode: "public_final",
          speakerSnapshot: { kind: "host", displayName: "Host" }, contextSnapshot: {},
          requestedConfig: {}, effectiveConfig: {}, createdAt: now },
      }, 202);
    }
    if (path === "/v1/skills" && method === "GET") {
      return fulfill(route, {
        skills: [
          { skillId: "cell-qc", name: "cell-qc", description: "QC tables",
            heroBound: false, boundBy: [], sourceInfo: { source: "user", scope: "user" } },
          { skillId: "review-guard", name: "review-guard", description: "Bound to the reviewer hero",
            heroBound: true, boundBy: ["security-reviewer"], sourceInfo: { source: "builtin", scope: "builtin" } },
          { skillId: "project-only", name: "project-only", description: "Project scope",
            heroBound: false, boundBy: [], sourceInfo: { source: "project", scope: "project" } },
        ],
        diagnostics: [],
      });
    }
    return route.abort();
  });
}

/**
 * Wait until the composer has the Hero and Skill catalogues. Under parallel load
 * the first `@` can be typed before the fetches land, which makes a token test
 * flaky for reasons that have nothing to do with the token.
 */
async function awaitCatalogue(page: Page) {
  const plus = page.getByRole("button", { name: "Insert a hero, graph, skill, or file" });
  await plus.click();
  const picker = page.locator(".composer-picker");
  await expect(picker.getByRole("group", { name: "Heroes" })).toBeVisible();
  await expect(picker.getByRole("group", { name: "Skills" })).toBeVisible();
  await plus.click();
  await expect(picker).toBeHidden();
}

async function openSession(page: Page) {
  await mockBackend(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByRole("button", { name: session.title, exact: true }).click();
}

test("typing @hero completes a hero target and clears the token", async ({ page }) => {
  await openSession(page);
  await awaitCatalogue(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".composer-picker");

  await textarea.fill("@hero:sec");
  await expect(panel).toBeVisible();
  const heroes = panel.getByRole("group", { name: "Heroes" });
  await expect(heroes.getByText("@security-reviewer")).toBeVisible();

  await heroes.getByText("@security-reviewer").click();
  // The token is consumed and the hero becomes a pill on the attachment shelf.
  await expect(textarea).toHaveValue("");
  await expect(panel).not.toBeVisible();
  await expect(page.locator(".composer-pill.is-hero")).toContainText("@security-reviewer");
});

test("typing @graph picks a graph invocation as a pill", async ({ page }) => {
  await openSession(page);
  await awaitCatalogue(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".composer-picker");

  await textarea.fill("@graph:go-");
  await expect(panel).toBeVisible();
  const graphs = panel.getByRole("group", { name: "Graphs" });
  await expect(graphs.getByText("go-review")).toBeVisible();

  await graphs.getByText("go-review").click();
  // A graph invocation is a pill, not text the UI inserted into the draft.
  await expect(textarea).toHaveValue("");
  await expect(panel).not.toBeVisible();
  await expect(page.locator(".composer-pill.is-graph")).toContainText("go-review@2");
});

test("typing / lists global skills and keeps the bound ones out of reach", async ({ page }) => {
  await openSession(page);
  await awaitCatalogue(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".composer-picker");

  await textarea.fill("/cell");
  await expect(panel).toBeVisible();
  const skills = panel.getByRole("group", { name: "Skills" });
  await expect(skills.getByText("/cell-qc")).toBeVisible();

  await skills.getByText("/cell-qc").click();
  await expect(page.locator(".composer-pill.is-skill")).toContainText("/cell-qc");
  await expect(textarea).toHaveValue("");
});

test("a skill a hero already loads is listed read-only, not offered", async ({ page }) => {
  await openSession(page);
  await awaitCatalogue(page);
  const textarea = page.locator("textarea[aria-label]");

  // Clear the filter so every group is visible, then open the bound group.
  await textarea.fill("/");
  const panel = page.locator(".composer-picker");
  await expect(panel).toBeVisible();
  const boundHead = panel.getByRole("button", { name: /Loaded by a Hero/ });
  await boundHead.click();
  const bound = panel.getByRole("group", { name: /Loaded by a Hero/ });
  const row = bound.getByRole("option", { name: /review-guard/ });
  await expect(row).toHaveAttribute("aria-disabled", "true");
  // A read-only row is not selectable, so no Skill pill can appear from it.
  await expect(page.locator(".composer-pill.is-skill")).toHaveCount(0);
  await expect(row).toHaveAccessibleName(/security-reviewer/);
});

test("@ addressing does not open when no match exists", async ({ page }) => {
  await openSession(page);
  await awaitCatalogue(page);
  const textarea = page.locator("textarea[aria-label]");
  const panel = page.locator(".composer-picker");

  await textarea.fill("@hero:zzz");
  await expect(panel).not.toBeVisible();
  await textarea.fill("@graph:nope");
  await expect(panel).not.toBeVisible();
});

test("a picked skill rides on the turn and does not survive it", async ({ page }) => {
  let body: Record<string, unknown> | undefined;
  await mockBackend(page, (captured) => { body = captured; });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await awaitCatalogue(page);

  const textarea = page.locator("textarea[aria-label]");
  await textarea.fill("/cell");
  await page.locator(".composer-picker").getByRole("group", { name: "Skills" }).getByText("/cell-qc").click();
  await expect(page.locator(".composer-pill.is-skill")).toContainText("/cell-qc");
  // The token is consumed, so the draft holds only what the user typed.
  await expect(textarea).toHaveValue("");

  await textarea.fill("run the QC");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect.poll(() => body).toBeTruthy();
  expect(body).toMatchObject({ text: "run the QC", skills: [{ id: "cell-qc" }] });

  // One-shot: the next turn starts clean.
  await expect(page.locator(".composer-pill.is-skill")).toHaveCount(0);
});

test("a project-scope skill is never offered, because a turn cannot name it", async ({ page }) => {
  await mockBackend(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await awaitCatalogue(page);

  await page.locator("textarea[aria-label]").fill("/project");
  const skills = page.locator(".composer-picker").getByRole("group", { name: "Skills" });
  await expect(skills.getByText("/project-only")).toHaveCount(0);
});

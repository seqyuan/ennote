import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "ui-project", name: "UI review", description: "", status: "active",
  createdAt: "2026-08-06T00:00:00Z", updatedAt: "2026-08-06T00:00:00Z" };
const session = { id: "ui-session", projectId: project.id, title: "Review surface", status: "active",
  activeLeafMessageId: "m1", createdAt: "2026-08-06T00:00:00Z", updatedAt: "2026-08-06T00:00:00Z" };
const run = { id: "compaction-run", turnId: "turn", sessionId: session.id, runKind: "context_compaction",
  attempt: 1, status: "queued", commitFormatVersion: 1, executionDepth: 0, publishMode: "public_final",
  speakerSnapshot: { kind: "host", displayName: "Host" }, contextSnapshot: {}, requestedConfig: {}, effectiveConfig: {},
  createdAt: "2026-08-06T00:00:00Z" };

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function openConversation(page: Page) {
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
}

test("manual compaction uses an inline prompt bar and submits the focus instruction", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  let compactionBody: Record<string, unknown> | null = null;
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/sessions/${session.id}/compactions`) {
      if (route.request().method() === "POST") {
        compactionBody = JSON.parse(route.request().postData() ?? "{}");
        return fulfill(route, { runId: run.id, compactionId: "checkpoint", status: "queued", existing: false }, 202);
      }
      return fulfill(route, []);
    }
    if (path === `/v1/runs/${run.id}`) return fulfill(route, run);
    return route.abort();
  });
  await openConversation(page);

  // The compact button opens the inline bar instead of a native prompt.
  await page.getByRole("button", { name: "Compact context" }).click();
  await expect(page.getByText("Create context checkpoint")).toBeVisible();
  await expect(page.getByPlaceholder("Optional focus for the checkpoint…")).toBeVisible();

  // Cancel path closes the bar without submitting.
  await page.getByRole("dialog", { name: "Create context checkpoint" })
    .getByText("Cancel", { exact: true }).click();
  await expect(page.getByText("Create context checkpoint")).not.toBeVisible();
  expect(compactionBody).toBeNull();

  // Confirm path submits the focus instruction.
  await page.getByRole("button", { name: "Compact context" }).click();
  await page.getByPlaceholder("Optional focus for the checkpoint…").fill("Keep the conclusion section intact");
  await page.getByRole("button", { name: "Create checkpoint" }).click();
  await expect.poll(() => compactionBody).not.toBeNull();
  expect(compactionBody).toMatchObject({ baseMessageId: "m1", instructions: "Keep the conclusion section intact" });
});

test("creating a project uses a dialog instead of native prompts", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  let created: Record<string, unknown> | null = null;
  let projectCalls = 0;
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") {
      if (route.request().method() === "POST") {
        created = JSON.parse(route.request().postData() ?? "{}");
        return fulfill(route, {
          project: { ...project, id: "new-project", name: created?.name },
          workspace: { projectId: "new-project", hostPath: created?.hostPath, jail: "jail" },
        }, 201);
      }
      projectCalls += 1;
      return fulfill(route, projectCalls === 1 ? [] : [{ ...project, id: "new-project", name: "RNA screen" }]);
    }
    return route.abort();
  });

  await page.goto("/");
  await expect(page.getByText("Choose a project to start.")).toBeVisible();
  await page.getByRole("button", { name: "New Project" }).click();

  await expect(page.getByRole("dialog", { name: "New project" })).toBeVisible();
  await page.getByPlaceholder("RNA screen").fill("RNA screen");
  await page.getByPlaceholder("/data/projects/rna-screen").fill("/data/projects/rna-screen");
  await page.getByRole("button", { name: "Create project" }).click();

  await expect.poll(() => created).not.toBeNull();
  expect(created).toMatchObject({ name: "RNA screen", hostPath: "/data/projects/rna-screen" });
  // Dialog closes and the new project is auto-selected in the selector.
  await expect(page.getByRole("dialog", { name: "New project" })).not.toBeVisible();
  await expect(page.getByRole("button", { name: "RNA screen" })).toBeVisible();
});

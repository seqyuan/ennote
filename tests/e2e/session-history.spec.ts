import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "project", name: "History project", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "session", projectId: project.id, title: "Restored session", status: "active", activeLeafMessageId: "m3", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:03Z" };

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return {
    id, sessionId: session.id, parentMessageId, role, status: "complete",
    parts: [{ type: "text", text }], createdAt: `2026-07-28T00:00:0${id.slice(-1)}Z`,
  };
}

async function mockHistoryAPI(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    let data: unknown;
    if (path === "/v1/projects") data = [project];
    else if (path === `/v1/projects/${project.id}/sessions`) data = [session];
    else if (path === `/v1/sessions/${session.id}`) data = session;
    else if (path === `/v1/sessions/${session.id}/active-run`) data = null;
    else if (path === `/v1/sessions/${session.id}/compactions`) data = [];
    else if (path === `/v1/sessions/${session.id}/messages` && url.searchParams.has("before")) {
      data = { messages: [message("m1", undefined, "user", "oldest restored message")], hasMore: false, activeLeafMessageId: "m3" };
    } else if (path === `/v1/sessions/${session.id}/messages`) {
      data = {
        messages: [message("m2", "m1", "assistant", "middle restored message"), message("m3", "m2", "user", "latest restored message")],
        nextCursor: "older-page", hasMore: true, activeLeafMessageId: "m3",
      };
    } else {
      return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { message: path } }) });
    }
    await fulfill(route, data);
  });
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("opening a session restores and paginates its active message branch", async ({ page }) => {
  await mockHistoryAPI(page);
  await page.goto("/");
  await page.getByTitle("Select project").click();
  await page.getByText(project.name, { exact: true }).click();
  await page.getByText(session.title, { exact: true }).click();

  await expect(page.getByText("middle restored message", { exact: true })).toBeVisible();
  await expect(page.getByText("latest restored message", { exact: true })).toBeVisible();
  await expect(page.getByText("oldest restored message", { exact: true })).toHaveCount(0);

  await page.getByRole("button", { name: "Load earlier messages" }).click();
  await expect(page.getByText("oldest restored message", { exact: true })).toBeVisible();
  const ids = await page.locator("[data-message-id]").evaluateAll(elements => elements.map(element => element.getAttribute("data-message-id")));
  expect(ids).toEqual(["m1", "m2", "m3"]);
  await expect(page.getByRole("button", { name: "Load earlier messages" })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});

test("a late history response cannot overwrite a newly selected session", async ({ page }) => {
  const slow = { ...session, id: "slow", title: "Slow session", activeLeafMessageId: "slow-message" };
  const fast = { ...session, id: "fast", title: "Fast session", activeLeafMessageId: "fast-message" };
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [slow, fast]);
    if (path === `/v1/sessions/${slow.id}`) return fulfill(route, slow);
    if (path === `/v1/sessions/${fast.id}`) return fulfill(route, fast);
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/compactions")) return fulfill(route, []);
    if (path === `/v1/sessions/${slow.id}/messages`) {
      await new Promise(resolve => setTimeout(resolve, 250));
      return fulfill(route, { messages: [{ ...message("slow-message", undefined, "user", "stale session message"), sessionId: slow.id }], hasMore: false });
    }
    if (path === `/v1/sessions/${fast.id}/messages`) {
      return fulfill(route, { messages: [{ ...message("fast-message", undefined, "user", "current session message"), sessionId: fast.id }], hasMore: false });
    }
    return route.abort();
  });

  await page.goto("/");
  await page.getByTitle("Select project").click();
  await page.getByText(project.name, { exact: true }).click();
  await page.getByText(slow.title, { exact: true }).click();
  await page.getByText(fast.title, { exact: true }).click();
  await expect(page.getByText("current session message", { exact: true })).toBeVisible();
  await page.waitForTimeout(350);
  await expect(page.getByText("stale session message", { exact: true })).toHaveCount(0);
});

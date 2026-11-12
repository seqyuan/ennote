import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, selectProject, tryFulfillHarnessRoutes } from "./harness";

/**
 * "Open in the canvas": a message artifact moving into the inspector lane.
 *
 * Everything here runs against mocked Worker routes, because the subject is the
 * *client*: which tab a press creates, which pane it lands in, what survives a
 * reload, and what a deleted artifact leaves behind. The widget's sandbox and
 * envelope are the Worker's and are pinned by generative-canvas.spec.ts; the
 * fixture pages below only have to say the two words the client listens for
 * (`ready`, `height`) so a lane frame reaches the same state a real one does.
 */

const project = { id: "canvas-project", name: "Canvas project", description: "", status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
const other = { id: "other-canvas-project", name: "Other canvas project", description: "", status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
const session = { id: "canvas-session", projectId: project.id, title: "Canvas widgets", status: "active", activeLeafMessageId: "m4", activeBranchId: "main", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:04Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" }));

function workspaceOf(owner: { id: string }) {
  return { id: `workspace-${owner.id}`, projectId: owner.id, kind: "local", hostPath: `/tmp/${owner.id}`, virtualPath: "/workspace", status: "active", pathFingerprint: `fingerprint-${owner.id}`, createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
}

const widgets = [
  { artifactId: "chart", name: "chart.html", kind: "widget", mimeType: "text/html; charset=utf-8", sizeBytes: 512, sha256: "chart-sha" },
  { artifactId: "map", name: "map.html", kind: "widget", mimeType: "text/html; charset=utf-8", sizeBytes: 640, sha256: "map-sha" },
];

const messages = [
  part("m1", undefined, "user", [{ type: "text", text: "Draw me two canvases." }]),
  part("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "publish", name: "publish_artifact", arguments: { paths: ["/workspace/chart.html", "/workspace/map.html"] } } }]),
  part("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "publish", toolName: "publish_artifact", content: "Published two widgets", isError: false, artifacts: widgets } }]),
  part("m4", "m3", "assistant", [{ type: "text", text: "Both canvases are ready." }]),
];

function part(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[]) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", parts, createdAt: `2026-11-11T00:00:0${id.slice(-1)}Z` };
}

/** The stored row `getArtifact` answers with: a reference plus its provenance. */
function record(reference: (typeof widgets)[number]) {
  return {
    id: reference.artifactId, projectId: project.id, sessionId: session.id, messageId: "m3", runId: "run-1",
    name: reference.name, kind: reference.kind, mimeType: reference.mimeType, sizeBytes: reference.sizeBytes,
    sha256: reference.sha256, retentionClass: "project", createdAt: "2026-11-11T00:00:03Z",
  };
}

/**
 * A page that behaves like the Worker's envelope in the only way this spec can
 * observe: it says it is ready and reports a height. A frame that never answers
 * is generative-canvas.spec.ts's case, not this one.
 */
function widgetDocument(label: string) {
  return `<!doctype html><html><body style="margin:0">`
    + `<div data-canvas-body="${label}" style="height:160px">${label}</div>`
    + `<script>parent.postMessage({type:"ennote:widget:ready"},"*");`
    + `parent.postMessage({type:"ennote:widget:height",height:160},"*");</script>`
    + `</body></html>`;
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

/**
 * Answer every route the shell asks for, and count the metadata reads so a reload
 * can be shown to fetch again rather than trust a cache.
 */
async function mockCanvas(page: Page, options: { deleted?: string[] } = {}) {
  const reads: string[] = [];
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project, other]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/attention") return fulfill(route, { items: [], pendingCount: 0 });
    for (const owner of [project, other]) {
      if (path === `/v1/projects/${owner.id}/workspace`) return fulfill(route, workspaceOf(owner));
      if (path === `/v1/projects/${owner.id}/files`) return fulfill(route, []);
      if (path === `/v1/projects/${owner.id}/prompt-templates`) return fulfill(route, { templates: [], diagnostics: [] });
      if (path === `/v1/projects/${owner.id}/sessions`) return fulfill(route, owner.id === project.id ? [session] : []);
    }
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/recovery`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/messages`) {
      return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m4" });
    }
    if (path === `/v1/sessions/${session.id}/branches`) {
      return fulfill(route, [{ id: "main", sessionId: session.id, label: "Main", leafMessageId: "m4", messageCount: 4, active: true, createdAt: session.createdAt, updatedAt: session.updatedAt }]);
    }
    const artifact = path.match(new RegExp(`^/v1/sessions/${session.id}/artifacts/([^/]+)(/preview)?$`));
    if (artifact) {
      const [, artifactId, preview] = artifact;
      if (preview) {
        return route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: widgetDocument(artifactId) });
      }
      reads.push(artifactId);
      // A deleted artifact answers the Worker's own 404 envelope: the lane has to
      // read that as "this row is stale", not as a reason to drop the pane.
      if (options.deleted?.includes(artifactId)) {
        return route.fulfill({
          status: 404, contentType: "application/json",
          body: JSON.stringify({ error: { code: "artifact_not_found", message: "artifact not found" } }),
        });
      }
      const known = widgets.find(candidate => candidate.artifactId === artifactId);
      return known ? fulfill(route, record(known)) : route.fulfill({ status: 599, body: `unknown artifact ${artifactId}` });
    }
    // 599, not abort: an unmocked route must fail loudly instead of looking like a
    // network blip the App is designed to survive.
    return route.fulfill({ status: 599, body: `unmocked ${path}` });
  });
  return reads;
}

async function openTranscript(page: Page, options: { deleted?: string[] } = {}) {
  await page.setViewportSize({ width: 1440, height: 900 });
  const reads = await mockCanvas(page, options);
  // `openSession` seeds the stored pick instead of clicking the row: the startup
  // blank-session connect can land after a spec's own click and overwrite it, and
  // a canvas test that quarrels with that flow tests the wrong thing.
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.locator('[data-artifact-id="chart"]')).toBeVisible();
  return reads;
}

const openButton = (page: Page, name: string) => page.getByRole("button", { name: `Open ${name} in the canvas` });
const laneTab = (page: Page, name: string) => page.getByRole("tab", { name });

test("opens a message widget in the inspector lane inside the sandboxed frame", async ({ page }) => {
  const reads = await openTranscript(page);
  await openButton(page, "chart.html").click();

  await expect(laneTab(page, "chart.html")).toBeVisible();
  // The lane is the only pane, and the artifact it draws is the one that was
  // pressed: the message's own frame is a separate element with the same title.
  const pane = page.getByTestId("inspector-pane-focused");
  const frame = pane.locator('iframe[title="Interactive preview of chart.html"]');
  await expect(frame).toBeVisible();
  await expect(frame).toHaveAttribute("sandbox", "allow-scripts");
  await expect(frame).not.toHaveAttribute("sandbox", /same-origin/);
  // 15s, not the 5s default: `WIDGET_READY_TIMEOUT_MS` is also 5s, so a default
  // timeout would turn a slightly late handshake into a "stalled" state at the same
  // instant the assertion gave up, and the failure would name the wrong thing.
  await expect(pane.locator(".artifact-widget")).toHaveAttribute("data-widget-state", "ready", { timeout: 15_000 });
  // The body is inside the frame, so it is asked of the frame. Parent text queries
  // stop at the iframe boundary: the frame is an opaque origin, and that is the point.
  await expect(frame.contentFrame().locator('[data-canvas-body="chart"]')).toBeVisible();
  // The body was fetched through the Session-scoped metadata route, not reconstructed
  // from the row: the row only carries ids and a label.
  expect(reads).toContain("chart");
});

test("pressing the same artifact twice keeps one tab", async ({ page }) => {
  await openTranscript(page);
  await openButton(page, "chart.html").click();
  await expect(laneTab(page, "chart.html")).toBeVisible();
  await expect(page.getByTestId("inspector-pane-focused")).toBeVisible();

  await openButton(page, "chart.html").click();
  // Still one row, still one pane: opening an artifact that is already in the
  // strip moves the focus to it instead of drawing a second copy beside it.
  await expect(laneTab(page, "chart.html")).toHaveCount(1);
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);
});

test("two artifacts compare side by side in the two panes", async ({ page }) => {
  await openTranscript(page);
  await openButton(page, "chart.html").click();
  // The panel resolves to 680 at this viewport, which is the wide layout. Both panes
  // are a property of the layout, so assert the precondition here — after the panel
  // exists, and before the second artifact needs a side pane — rather than let a
  // narrower panel fail later as a mystery.
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide", { timeout: 15000 });
  await expect(page.getByTestId("inspector-pane-focused").locator("iframe"))
    .toHaveAttribute("title", "Interactive preview of chart.html");

  await openButton(page, "map.html").click();
  const main = page.getByTestId("inspector-pane-main");
  const side = page.getByTestId("inspector-pane-side");
  await expect(main).toBeVisible();
  await expect(side).toBeVisible();
  // Each pane renders its own artifact: the frame content is the proof, not the
  // tab label, since a wrong key would show one body under both strips.
  await expect(main.locator("iframe").contentFrame().locator('[data-canvas-body="chart"]')).toBeVisible();
  await expect(side.locator("iframe").contentFrame().locator('[data-canvas-body="map"]')).toBeVisible();
  await expect(main.locator("iframe")).toHaveAttribute("sandbox", "allow-scripts");
  await expect(side.locator("iframe")).toHaveAttribute("sandbox", "allow-scripts");
});

test("closing one pane's tab leaves the other pane alone", async ({ page }) => {
  await openTranscript(page);
  await openButton(page, "chart.html").click();
  await openButton(page, "map.html").click();
  await expect(page.getByTestId("inspector-pane-side")).toBeVisible();

  await page.getByRole("button", { name: "Close map.html" }).click();
  await expect(laneTab(page, "map.html")).toHaveCount(0);
  // One pane is left, still showing the artifact that was in the other half.
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);
  await expect(page.getByTestId("inspector-pane-focused").locator("iframe"))
    .toHaveAttribute("title", "Interactive preview of chart.html");
  // Closing a tab is a lane gesture: the panel and the transcript stay.
  await expect(page.getByTestId("right-panel-lanes")).toBeVisible();
  await expect(page.locator('[data-artifact-id="map"]')).toBeVisible();
});

test("a reload restores the artifact tab and fetches its body again", async ({ page }) => {
  const reads = await openTranscript(page);
  await openButton(page, "chart.html").click();
  await expect(laneTab(page, "chart.html")).toBeVisible();
  const before = reads.filter(id => id === "chart").length;

  await page.reload();
  // The row is the Project's, not the session's: it comes back from storage and the
  // lane draws it without anyone pressing again.
  await expect(laneTab(page, "chart.html")).toBeVisible({ timeout: 15000 });
  await expect.poll(() => reads.filter(id => id === "chart").length, { timeout: 15000 })
    .toBeGreaterThan(before);
  await expect(page.getByTestId("inspector-pane-focused").locator("iframe"))
    .toHaveAttribute("title", "Interactive preview of chart.html");
  await expect(page.locator('[data-artifact-id="chart"]')).toBeVisible();
});

test("a deleted artifact reports inside its pane and takes nothing else down", async ({ page }) => {
  await openTranscript(page, { deleted: ["chart"] });
  await openButton(page, "chart.html").click();

  const pane = page.getByTestId("inspector-pane-focused");
  await expect(pane.getByRole("alert")).toContainText("could not be loaded");
  // The cause is the Worker's own words, not ours: this surface must not guess
  // between "deleted", "network", and "the Worker restarted".
  await expect(pane.getByRole("alert")).toContainText("artifact not found");
  // A transient failure must not strand the pane: retry is the only way out,
  // because the row's id cannot change.
  await expect(pane.getByRole("button", { name: "Retry" })).toBeVisible();
  await expect(pane.locator("iframe")).toHaveCount(0);
  // A stale row is the row's problem: the strip, the panel and the conversation
  // all stay, so the reader can close it or go back to the message.
  await expect(laneTab(page, "chart.html")).toBeVisible();
  await expect(page.getByTestId("right-panel-lanes")).toBeVisible();
  await expect(page.locator('[data-artifact-id="chart"]')).toBeVisible();
  await expect(page.getByText("Both canvases are ready.")).toBeVisible();
});

test("another Project's lane never shows this Project's artifact rows", async ({ page }) => {
  await openTranscript(page);
  await openButton(page, "chart.html").click();
  await expect(laneTab(page, "chart.html")).toBeVisible();

  await selectProject(page, other.name);
  // The panel closed with the Project it belongs to; open it here so the assertion
  // is about the lane's contents and not about the shell being collapsed.
  await page.getByTitle("Show panel").click();
  await expect(page.getByTestId("right-panel-lanes")).toBeVisible();
  await expect(laneTab(page, "chart.html")).toHaveCount(0);
  await expect(page.getByTestId("inspector-pane-focused")).toHaveCount(0);

  // The row was not deleted by the switch: it belongs to the first Project.
  await selectProject(page, project.name);
  await expect(laneTab(page, "chart.html")).toBeVisible({ timeout: 15000 });
});

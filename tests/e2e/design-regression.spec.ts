import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";

/**
 * Design-contract regression.
 *
 * Pixel snapshots are platform-dependent, so this suite snapshots the
 * *computed style contract* instead: tokens, surfaces, radii, typography, and
 * geometry that must stay stable across light/dark and breakpoints. It is
 * generated from real CSS in Chromium, but contains only serializable style
 * values, so Linux CI can compare against a baseline created on macOS.
 */

const project = { id: "design-project", name: "Design contract", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const workspace = { id: "design-workspace", projectId: project.id, kind: "local", hostPath: "/tmp/design-project", virtualPath: "/workspace", status: "active", pathFingerprint: "fp", createdAt: "2026-07-28T00:00:00Z" };
const session = { id: "design-session", projectId: project.id, title: "Design review", status: "active", activeLeafMessageId: "m3", activeBranchId: "main", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:03Z" };
const branch = { id: "main", sessionId: session.id, label: "Main", leafMessageId: "m3", messageCount: 3, active: true, createdAt: session.createdAt, updatedAt: session.updatedAt };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));
const provider = { id: "design-provider", name: "DeepSeek", providerType: "openai-compatible", baseUrl: "https://api.deepseek.com", credentialConfigured: true, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const model = { id: "design-model", providerId: provider.id, displayName: "DeepSeek V4", modelName: "deepseek-v4", supportsThinking: true, supportsVision: true, maxContextTokens: 128000, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[]) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", parts, createdAt: "2026-07-28T00:00:00Z" };
}

const messages = [
  message("m1", undefined, "user", [{ type: "text", text: "Inspect the marker matrix and update the report." }]),
  message("m2", "m1", "assistant", [
    { type: "text", text: "The matrix is valid.\n\n| Check | Status |\n| --- | --- |\n| Schema | pending |" },
    { type: "tool_call", toolCall: { id: "read-call", name: "read", arguments: { path: "/workspace/markers.csv" } } },
  ]),
  message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "read-call", toolName: "read", content: "gene,cluster\nEPCAM,C1", isError: false } }]),
];

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockDesign(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m3" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/branches`) return fulfill(route, [branch]);
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: `data: ${JSON.stringify({ type: "subscribed", instanceId: "design", lastSeq: 0, snapshot: { activeRun: null, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false } })}\n\n`,
    });
    if (path === "/v1/attention") return fulfill(route, []);
    return route.abort();
  });
}

async function styleOf(page: Page, selector: string, properties: string[]) {
  return page.locator(selector).first().evaluate((element, props) => {
    const computed = getComputedStyle(element);
    return Object.fromEntries(props.map((property) => [property, computed.getPropertyValue(property)]));
  }, properties);
}

async function rootTokens(page: Page) {
  return page.evaluate(() => {
    const computed = getComputedStyle(document.documentElement);
    const token = (name: string) => computed.getPropertyValue(name).trim();
    return {
      accent: token("--accent"),
      surface0: token("--surface-0"),
      surface1: token("--surface-1"),
      content1: token("--content-1"),
      content2: token("--content-2"),
      content3: token("--content-3"),
      strokeHairline: token("--stroke-hairline"),
      radiusXs: token("--radius-xs"),
      radiusSm: token("--radius-sm"),
      radiusMd: token("--radius-md"),
      radiusPanel: token("--radius-panel"),
      textXs: token("--text-xs"),
      textSm: token("--text-sm"),
      textBase: token("--text-base"),
      textLg: token("--text-lg"),
    };
  });
}

test("design contract stays token-driven in light and dark", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockDesign(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.locator(".composer-editor")).toBeVisible();
  await expect(page.locator(".tool-batch")).toBeVisible();

  const root = await rootTokens(page);
  expect(root.radiusPanel).toBe("24px");
  expect(root.textXs).toBe("11px");

  const light = {
    root,
    topbar: await styleOf(page, ".app-topbar", ["height", "background-color", "border-bottom-color"]),
    sidebar: await styleOf(page, ".sidebar-container", ["background-color", "border-right-color"]),
    composer: await styleOf(page, ".composer-editor", ["border-radius", "background-color", "border-color", "box-shadow"]),
    branch: await styleOf(page, ".branch-trigger", ["height", "border-radius", "background-color", "border-color", "color"]),
    toolBatch: await styleOf(page, ".tool-batch", ["background-color", "border-top-color", "border-bottom-color"]),
    userBubble: await styleOf(page, ".user-message", ["border-radius", "background-color"]),
  };
  await expect(JSON.stringify(light, null, 2)).toMatchSnapshot("design-contract-light.txt");

  await page.getByRole("button", { name: "Settings", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Settings" });
  await expect(dialog).toBeVisible();
  const settings = {
    shell: await styleOf(page, ".settings-dialog-shell", ["width", "height", "border-radius", "background-color", "box-shadow"]),
    navtitle: await styleOf(page, ".settings-dialog-navtitle", ["font-size", "font-weight", "color"]),
    search: await styleOf(page, ".settings-dialog-search", ["height", "border-radius", "border-color"]),
    tab: await styleOf(page, ".settings-dialog-tab.active", ["height", "border-radius", "background-color", "font-size"]),
  };
  await expect(JSON.stringify(settings, null, 2)).toMatchSnapshot("design-contract-settings.txt");
  await page.keyboard.press("Escape");

  await page.getByRole("button", { name: "Switch to dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  const dark = {
    root: await rootTokens(page),
    topbar: await styleOf(page, ".app-topbar", ["height", "background-color", "border-bottom-color"]),
    sidebar: await styleOf(page, ".sidebar-container", ["background-color", "border-right-color"]),
    composer: await styleOf(page, ".composer-editor", ["border-radius", "background-color", "border-color", "box-shadow"]),
    branch: await styleOf(page, ".branch-trigger", ["height", "border-radius", "background-color", "border-color", "color"]),
    toolBatch: await styleOf(page, ".tool-batch", ["background-color", "border-top-color", "border-bottom-color"]),
    userBubble: await styleOf(page, ".user-message", ["border-radius", "background-color"]),
  };
  await expect(JSON.stringify(dark, null, 2)).toMatchSnapshot("design-contract-dark.txt");
});

test("layout contract holds across desktop, tablet, and mobile", async ({ page }) => {
  await mockDesign(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.locator(".composer-editor")).toBeVisible();

  for (const width of [1440, 1024, 768, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(page.locator(".composer-editor")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  }

  // Tablet: the right panel is a full-screen drawer, not a squeezed column.
  await page.setViewportSize({ width: 800, height: 900 });
  await page.getByTitle("Show panel").click();
  const panel = page.locator(".right-panel-container");
  await expect(panel).toBeVisible();
  const box = await panel.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(799);
  await expect(page.getByRole("button", { name: "Back to conversation" })).toBeVisible();
  await page.getByRole("button", { name: "Back to conversation" }).click();

  // Sidebar row actions keep a 24px hit target and appear for keyboard focus.
  await page.setViewportSize({ width: 1280, height: 900 });
  if (await page.getByRole("button", { name: "Open navigation" }).count() > 0) {
    await page.getByRole("button", { name: "Open navigation" }).click();
  }
  const row = page.locator(".sb-project-row").first();
  await row.hover();
  const action = row.locator(".sb-row-icon-btn").first();
  await expect(action).toBeVisible();
  const actionBox = await action.boundingBox();
  expect(actionBox?.width ?? 0).toBeGreaterThanOrEqual(24);
  expect(actionBox?.height ?? 0).toBeGreaterThanOrEqual(24);

  // Mobile keeps the search field out of the bottom tab bar.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Search settings" })).toBeHidden();
});

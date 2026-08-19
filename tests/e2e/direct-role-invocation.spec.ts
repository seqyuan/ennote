import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-03T00:00:00Z";
const project = { id: "role-project", name: "Role project", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "role-session", projectId: project.id, title: "Role review", status: "active", mode: "hosted",
  activeLeafMessageId: "role-answer", createdAt: now, updatedAt: now };
const model = { id: "model", providerId: "provider", modelName: "model", displayName: "Default Model", contextWindow: 32000,
  maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: false, thinkingDialect: "none",
  supportedThinkingEfforts: ["default"], isDefault: true, status: "active", createdAt: now, updatedAt: now };
const policies = ["discuss", "ask", "auto"].map((mode) => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool",
  version: 1, config: { mode }, status: "active", createdAt: now, updatedAt: now }));
const role = { id: "security-role", handle: "security-reviewer", name: "Security Reviewer", description: "Independent review",
  positioning: "Inspect trust boundaries.", icon: "shield-check", color: "#b91c1c", scope: "project", projectId: project.id,
  status: "active", currentVersionId: "security-v1", currentVersion: 1, updatedAt: now };
const messages = [
  { id: "question", sessionId: session.id, role: "user", status: "complete", speakerKind: "user",
    speakerSnapshot: { kind: "user", displayName: "You" }, addresseeKind: "role", addresseeObjectId: role.id,
    addresseeVersionId: role.currentVersionId, visibility: "public", parts: [{ type: "text", text: "Review this boundary." }], createdAt: now },
  { id: "role-answer", sessionId: session.id, parentMessageId: "question", role: "assistant", status: "complete", runId: "old-run",
    speakerKind: "role", speakerObjectId: role.id, speakerVersionId: role.currentVersionId, participantInstanceId: "participant",
    speakerSnapshot: { kind: "role", objectId: role.id, versionId: role.currentVersionId, handle: role.handle,
      displayName: role.name, color: role.color }, addresseeKind: "room", visibility: "public",
    parts: [{ type: "text", text: "The boundary is explicit." }], createdAt: now },
];

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockDirectRole(page: Page, onInvocation: (body: Record<string, unknown>) => void) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/roles") return fulfill(route, { items: [role], nextCursor: "" });
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "role-answer" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/invocations`) {
      onInvocation(route.request().postDataJSON());
      return fulfill(route, { turnId: "new-turn", userMessageId: "new-user", existing: false,
        run: { id: "new-run", sessionId: session.id, runKind: "agent", attempt: 1, status: "queued",
          commitFormatVersion: 2, executionDepth: 0, publishMode: "public_final",
          speakerSnapshot: { kind: "role", handle: role.handle, displayName: role.name }, contextSnapshot: {},
          requestedConfig: {}, effectiveConfig: {}, createdAt: now } }, 202);
    }
    if (path === "/v1/runs/new-run/events") {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: 'data: {"type":"run_succeeded","payload":{}}\n\n' });
    }
    return route.abort();
  });
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`direct Role target is structured and attributed at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    let invocation: Record<string, unknown> | undefined;
    await mockDirectRole(page, (body) => { invocation = body; });
    await page.goto("/");
    if (viewport.width <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
    await page.getByTitle("Select project").first().click();
    await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
    if (viewport.width <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
    await page.getByRole("button", { name: session.title, exact: true }).click();

    await expect(page.getByText(`@${role.handle}`, { exact: true }).first()).toBeVisible();
    await expect(page.getByText("The boundary is explicit.")).toBeVisible();
    await page.getByRole("button", { name: "Configure run", exact: true }).click();
    await page.getByTitle("Invocation target").click();
    await page.getByRole("option", { name: new RegExp(role.handle) }).click();
    await expect(page.getByTitle("Default Model")).toBeDisabled();
    await expect(page.getByRole("button", { name: "Ask", exact: true })).toBeDisabled();
    await page.getByRole("textbox", { name: "Message the agent" }).fill("Check the new policy.");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect.poll(() => invocation).toBeTruthy();
    expect(invocation).toMatchObject({ text: "Check the new policy.", target: { kind: "role", objectId: role.id,
      versionId: role.currentVersionId, contextMode: "room" } });
    expect(invocation).not.toHaveProperty("config");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });
}

import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, selectProject, tryFulfillHarnessRoutes } from "./harness";

/**
 * The Canvas channel: pinned artifacts in the right panel.
 *
 * Pin state is Worker state, so this spec mocks the three pin routes and lets
 * the client prove the flow on top of them: pin from the transcript, see the
 * card in the channel, open it into the inspector lane, unpin it away, and show
 * that a reload and a Project switch both read the server's answer rather than
 * a client cache. Every unmocked route answers 599 — never `route.abort()` — so
 * a missing route fails loudly instead of looking like a network blip.
 */

const project = { id: "canvas-pin-project", name: "Canvas pin project", description: "", status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
const other = { id: "other-pin-project", name: "Other pin project", description: "", status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
const session = { id: "canvas-pin-session", projectId: project.id, title: "Canvas pins", status: "active", activeLeafMessageId: "m4", activeBranchId: "main", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:04Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" }));

function workspaceOf(owner: { id: string }) {
  return { id: `workspace-${owner.id}`, projectId: owner.id, kind: "local", hostPath: `/tmp/${owner.id}`, virtualPath: "/workspace", status: "active", pathFingerprint: `fingerprint-${owner.id}`, createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
}

// A `file` kind has no preview body, so the ArtifactView renders just its header
// (name, kind, pin toggle) — which is all this spec needs to observe.
const artifact = { artifactId: "chart", name: "chart.html", kind: "file", mimeType: "application/octet-stream", sizeBytes: 512, sha256: "chart-sha" };
// The other Project's pre-existing pin, used to prove cross-Project scoping.
const otherArtifact = { artifactId: "other-chart", name: "other-chart.html", kind: "file", mimeType: "application/octet-stream", sizeBytes: 512, sha256: "other-sha" };

const messages = [
  part("m1", undefined, "user", [{ type: "text", text: "Pin this chart." }]),
  part("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "publish", name: "publish_artifact", arguments: { paths: ["/workspace/chart.html"] } } }]),
  part("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "publish", toolName: "publish_artifact", content: "Published one chart", isError: false, artifacts: [artifact] } }]),
  part("m4", "m3", "assistant", [{ type: "text", text: "The chart is ready." }]),
];

function part(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[]) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", parts, createdAt: `2026-11-11T00:00:0${id.slice(-1)}Z` };
}

/** The stored row `getArtifact` answers with: a reference plus its provenance. */
function record(reference: typeof artifact, pinnedAt?: string) {
  return {
    id: reference.artifactId, projectId: project.id, sessionId: session.id, messageId: "m3", runId: "run-1",
    name: reference.name, kind: reference.kind, mimeType: reference.mimeType, sizeBytes: reference.sizeBytes,
    sha256: reference.sha256, retentionClass: "project", createdAt: "2026-11-11T00:00:03Z",
    pinnedAt,
  };
}

/** The other Project's pinned artifact, with its own session and project. */
function otherRecord() {
  return {
    id: otherArtifact.artifactId, projectId: other.id, sessionId: "other-session", messageId: "om3", runId: "orun-1",
    name: otherArtifact.name, kind: otherArtifact.kind, mimeType: otherArtifact.mimeType, sizeBytes: otherArtifact.sizeBytes,
    sha256: otherArtifact.sha256, retentionClass: "project", createdAt: "2026-11-11T00:00:03Z", pinnedAt: "2026-11-12T00:00:00Z",
  };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

/**
 * Answer every route the shell asks for. Pin state lives in `pinned`, a Map of
 * artifactId → pinnedAt, so a reload reads the same server fact a real Worker
 * would answer with.
 */
async function mockCanvasPin(page: Page) {
  const pinned = new Map<string, string>();
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const method = route.request().method();
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
      if (path === `/v1/projects/${owner.id}/pinned-artifacts`) {
        // The canvas is per-Project: each Project answers with its own pins.
        if (owner.id === other.id) return fulfill(route, { artifacts: [otherRecord()] });
        const artifacts = [...pinned.entries()]
          .map(([, at]) => record(artifact, at))
          .sort((a, b) => String(b.pinnedAt ?? "").localeCompare(String(a.pinnedAt ?? "")));
        return fulfill(route, { artifacts });
      }
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
    // Pin and unpin both answer 200 with the refreshed artifact.
    const pin = path.match(new RegExp(`^/v1/sessions/${session.id}/artifacts/([^/]+)/pin$`));
    if (pin) {
      const [, artifactId] = pin;
      if (method === "POST") pinned.set(artifactId, "2026-11-12T09:00:00Z");
      else if (method === "DELETE") pinned.delete(artifactId);
      return fulfill(route, record(artifact, pinned.get(artifactId)));
    }
    const read = path.match(new RegExp(`^/v1/sessions/${session.id}/artifacts/([^/]+)$`));
    if (read && method === "GET") {
      return fulfill(route, record(artifact, pinned.get(read[1])));
    }
    // 599, not abort: an unmocked route must fail loudly instead of looking like a
    // network blip the App is designed to survive.
    return route.fulfill({ status: 599, body: `unmocked ${path}` });
  });
}

async function openTranscript(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockCanvasPin(page);
  // `openSession` seeds the stored pick so the blank-session startup race cannot
  // overwrite the spec's own selection.
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.locator('[data-artifact-id="chart"]')).toBeVisible();
}

/** Pin the transcript's artifact and land on the Canvas channel. */
async function pinFromTranscript(page: Page) {
  await page.getByRole("button", { name: "Pin chart.html to the canvas" }).click();
  await expect(page.getByRole("button", { name: "Unpin chart.html from the canvas" }))
    .toHaveAttribute("aria-pressed", "true");
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Canvas" }).click();
}

test("pinning an artifact in the transcript shows it in the canvas channel", async ({ page }) => {
  await openTranscript(page);
  // The toggle is disabled until the Worker answers getArtifact; clicking waits
  // for actionability, which is exactly the read-back this pins.
  await page.getByRole("button", { name: "Pin chart.html to the canvas" }).click();
  await expect(page.getByRole("button", { name: "Unpin chart.html from the canvas" }))
    .toHaveAttribute("aria-pressed", "true");

  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Canvas" }).click();
  await expect(page.locator(".right-panel-canvas-card-name", { hasText: "chart.html" })).toBeVisible();
});

test("clicking a canvas card opens the artifact in the inspector lane", async ({ page }) => {
  await openTranscript(page);
  await pinFromTranscript(page);

  await page.getByRole("button", { name: "Open chart.html", exact: true }).click();
  await expect(page.getByRole("tab", { name: "chart.html" })).toBeVisible();
});

test("unpinning via the canvas card removes it from the channel", async ({ page }) => {
  await openTranscript(page);
  await pinFromTranscript(page);

  const card = page.locator(".right-panel-canvas-card").filter({ hasText: "chart.html" });
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "Unpin chart.html" }).click();
  await expect(card).toHaveCount(0);
  await expect(page.getByText("Nothing pinned yet.")).toBeVisible();
});

test("a pin survives a reload because it is Worker state", async ({ page }) => {
  await openTranscript(page);
  await pinFromTranscript(page);

  await page.reload();
  // After reload the transcript's toggle re-reads getArtifact and still sees the
  // pin; the client cache is not the source of truth.
  await expect(page.locator('[data-artifact-id="chart"]')).toBeVisible({ timeout: 15000 });
  await expect(page.getByRole("button", { name: "Unpin chart.html from the canvas" }))
    .toHaveAttribute("aria-pressed", "true", { timeout: 15000 });

  // The shell was open when we reloaded, so it restores open; the Canvas tab is
  // already reachable without re-opening it.
  await page.getByRole("tab", { name: "Canvas" }).click();
  await expect(page.locator(".right-panel-canvas-card-name", { hasText: "chart.html" })).toBeVisible();
});

test("a pin in another Project does not appear in this Project's canvas channel", async ({ page }) => {
  await openTranscript(page);

  // This Project has nothing pinned, so its canvas channel is empty even though
  // the other Project has a pin of its own.
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Canvas" }).click();
  await expect(page.getByText("Nothing pinned yet.")).toBeVisible();
  await expect(page.locator(".right-panel-canvas-card-name", { hasText: "other-chart.html" })).toHaveCount(0);

  // The other Project's pin exists, and it is scoped to that Project.
  await selectProject(page, other.name);
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Canvas" }).click();
  await expect(page.locator(".right-panel-canvas-card-name", { hasText: "other-chart.html" })).toBeVisible();
});

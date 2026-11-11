import { rmSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillHarnessRoutes } from "./harness";

/**
 * End-to-end coverage for the generative canvas.
 *
 * The first test runs against the real Worker and the real envelope on purpose:
 * the CSP, the receiver script and the height protocol are the Worker's, so a
 * mocked response would only prove that a fixture can post a message. Here the
 * browser enforces the actual policy header and the actual script runs, which is
 * the only way to catch "the sandbox got looser" or "the receiver stopped
 * reporting" as a test failure rather than a product surprise.
 *
 * The timeout case is the exception: the real envelope always reports ready, so a
 * silent frame cannot be produced without hanging one. It uses a mock.
 */
const AUTO_TOOL_POLICY = "builtin-tool-auto-v1";

function startFakeOpenAI(): Promise<{ baseURL: string; close: () => Promise<void> }> {
  let calls = 0;
  const toolCalls = [
    { id: "call-good", path: "/workspace/good.html" },
    { id: "call-throws", path: "/workspace/throws.html" },
    { id: "call-external", path: "/workspace/external.html" },
  ];

  const server: Server = createServer((req, res) => {
    if (!(req.url ?? "").includes("/chat/completions")) {
      res.writeHead(404);
      res.end();
      return;
    }
    calls += 1;
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    if (calls === 1) {
      // Round one: publish three widgets and one table, so every rendering path a
      // single tool result can carry is exercised in one run.
      const deltas = [
        ...toolCalls.map((call, index) => ({
          index, id: call.id, type: "function",
          function: { name: "publish_artifact", arguments: JSON.stringify({ path: call.path, kind: "widget" }) },
        })),
        { index: 3, id: "call-csv", type: "function",
          function: { name: "publish_artifact", arguments: JSON.stringify({ path: "/workspace/data.csv" }) } },
        { index: 4, id: "call-text", type: "function",
          function: { name: "publish_artifact", arguments: JSON.stringify({ path: "/workspace/notes.txt" }) } },
        { index: 5, id: "call-static", type: "function",
          function: { name: "publish_artifact", arguments: JSON.stringify({ path: "/workspace/page.html" }) } },
      ];
      for (const delta of deltas) {
        res.write(`data: ${JSON.stringify({
          id: "e2e", model: "e2e-echo",
          choices: [{ delta: { tool_calls: [delta] }, finish_reason: null }],
        })}\n\n`);
      }
      res.write(`data: ${JSON.stringify({ id: "e2e", model: "e2e-echo",
        choices: [{ delta: {}, finish_reason: "tool_calls" }] })}\n\n`);
    } else {
      res.write(`data: ${JSON.stringify({ id: "e2e", model: "e2e-echo",
        choices: [{ delta: { content: "published the canvas" }, finish_reason: null }] })}\n\n`);
      res.write(`data: ${JSON.stringify({ id: "e2e", model: "e2e-echo",
        choices: [{ delta: {}, finish_reason: "stop" }] })}\n\n`);
    }
    res.write("data: [DONE]\n\n");
    res.end();
  });

  return new Promise((resolve, reject) => {
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        reject(new Error("fake OpenAI server did not bind a port"));
        return;
      }
      resolve({
        baseURL: `http://127.0.0.1:${address.port}/v1`,
        close: () => new Promise(done => {
          // A run that fails mid-stream leaves an open SSE request; without this the
          // close waits out the test timeout and hides the real failure.
          server.closeAllConnections?.();
          server.close(() => done());
        }),
      });
    });
  });
}

async function workerData<T>(request: APIRequestContext, method: "get" | "post", path: string,
  origin: string, body?: unknown): Promise<T> {
  const headers = { Origin: origin, Accept: "application/json" };
  const response = method === "get"
    ? await request.get(`/api/worker${path}`, { headers })
    : await request.post(`/api/worker${path}`, { headers, data: body });
  const json = await response.json() as { data?: T; error?: { message?: string } };
  expect(response.ok(), `${path} ${response.status()}: ${JSON.stringify(json)}`).toBeTruthy();
  return json.data as T;
}

async function openSession(page: Page, sessionId: string, projectName: string) {
  await page.goto("/");
  await page.getByTestId("ennote-shell").waitFor();
  await page.evaluate(id => localStorage.setItem("ennote-selected-session", id), sessionId);
  await page.goto("/");
  await page.getByTestId("ennote-shell").waitFor();
  await selectProject(page, projectName);
}

test("a published widget runs inside the Worker's own envelope", async ({ page, request }) => {
  // This one writes provider/model/project rows into the Worker it runs against, so
  // it refuses to run against an instance nobody chose. `playwright.config.ts` falls
  // back to the published default port, where an operator's real instance lives.
  test.skip(!process.env.PLAYWRIGHT_BASE_URL, "set PLAYWRIGHT_BASE_URL=http://127.0.0.1:45331 (a scratch gate)");
  // A live run: provider handshake, one tool round, three frames booting.
  test.setTimeout(180_000);
  const llm = await startFakeOpenAI();
  let workspace: string | null = null;
  let providerId: string | null = null;
  let modelId: string | null = null;
  let projectId: string | null = null;
  let originRef = "";
  try {
    await page.goto("/");
    const origin = new URL(page.url()).origin;
    originRef = origin;
    const stamp = Date.now();
    const provider = await workerData<{ id: string }>(request, "post", "/v1/provider-profiles", origin, {
      name: "E2E Canvas", key: `e2e-canvas-${stamp}`, providerType: "openai-compatible",
      baseUrl: llm.baseURL, apiKey: "e2e-key",
    });
    providerId = provider.id;
    const model = await workerData<{ id: string }>(request, "post", "/v1/model-profiles", origin, {
      providerId: provider.id, modelName: "e2e-echo", displayName: "E2E Canvas",
      contextWindow: 8000, maxOutputTokens: 256, supportsToolUse: true,
      thinkingDialect: "none", supportedThinkingEfforts: ["default"], isDefault: false,
    });
    modelId = model.id;
    const listing = await workerData<{ roots: string[] }>(request, "get", "/v1/host/directories", origin);
    workspace = (await workerData<{ path: string }>(request, "post", "/v1/host/directories", origin, {
      path: listing.roots[0], name: `ennote-e2e-canvas-${stamp}`,
    })).path;
    const project = (await workerData<{ project: { id: string; name: string } }>(
      request, "post", "/v1/projects", origin, { name: `Canvas ${stamp}`, hostPath: workspace })).project;
    projectId = project.id;
    const session = await workerData<{ id: string }>(request, "post",
      `/v1/projects/${project.id}/sessions`, origin, { title: `Canvas ${stamp}` });

    // The files are written through the project-file API, so the run only has to
    // call `publish_artifact` once per file: one tool round, no approval (the run
    // asks for the auto policy), and the published bytes are exactly these.
    const pages: Record<string, string> = {
      "/workspace/good.html": `<!doctype html><div style="height:600px;background:var(--color-background-secondary)">canvas body</div><script>document.title="widget-ok"</script>`,
      "/workspace/throws.html": `<!doctype html><div style="height:140px">partial drawing</div><script>throw new Error("e2e-boom")</script>`,
      // A host that resolves, so a failed load means the policy refused it rather
      // than the network being unable to reach it.
      "/workspace/external.html": `<!doctype html><div style="height:140px">external drawing</div><script src="https://example.com/widget.js"></script>`,
      "/workspace/data.csv": "gene,value\nEPCAM,7\n",
      "/workspace/notes.txt": "plain notes\n",
      // Same extension, no kind: this one must stay script-free. If `widget` ever
      // became the default for .html, this assertion is what catches it.
      "/workspace/page.html": `<!doctype html><h1>static page</h1><script>document.title="ran"</script>`,
    };
    for (const [path, content] of Object.entries(pages)) {
      await workerData(request, "post", `/v1/projects/${project.id}/files/create`, origin,
        { path, kind: "file", content });
    }

    // The browser's own CSP report is the assertion that matters: without it, a
    // policy loosened to `script-src *` (or removed) would leave every other
    // expectation in this test passing.
    const violations: string[] = [];
    page.on("console", message => {
      const text = message.text();
      if (/Content Security Policy/i.test(text)) violations.push(text);
    });

    await openSession(page, session.id, project.name);
    await workerData(request, "post", `/v1/sessions/${session.id}/turns`, origin, {
      text: "publish the canvas",
      // The model is named per run, so the Worker's default model — an operator
      // setting this test has no business changing — is left alone.
      config: { toolPolicyProfileId: AUTO_TOOL_POLICY, modelProfileId: modelId },
    });
    await expect(page.getByText("published the canvas")).toBeVisible({ timeout: 60_000 });

    // Several kinds coexist in one message; the widget is the one that renders in a
    // frame. `data-widget-state` lives on the frame wrapper, inside the artifact
    // section, because it describes the frame and not the artifact row.
    const widgets = page.locator('[data-artifact-kind="widget"]');
    await expect(widgets).toHaveCount(3);
    await expect(page.locator('[data-artifact-kind="table"]')).toHaveCount(1);
    await expect(page.locator('[data-artifact-kind="text"]')).toHaveCount(1);
    // The neighbouring .html went through the other branch, unchanged: no scripts.
    const staticPage = page.locator('[data-artifact-kind="static_html"]');
    await expect(staticPage).toHaveCount(1);
    await expect(staticPage.locator("iframe")).toHaveAttribute("sandbox", "");
    const frameCard = (name: string) => widgets
      .filter({ has: page.locator(`iframe[title*="${name}"]`) })
      .locator(".artifact-widget");

    const good = frameCard("good.html");
    const goodFrame = good.locator("iframe");
    await goodFrame.scrollIntoViewIfNeeded();
    // `ready` is posted by the Worker's receiver script, so reaching it proves the
    // real document loaded and executed inside the sandbox.
    await expect(good).toHaveAttribute("data-widget-state", "ready", { timeout: 30_000 });
    await expect(goodFrame).toHaveAttribute("sandbox", "allow-scripts");
    // The widget drew 600px of content; the frame reports it back and the client
    // applies it. A fixed fallback height would fail this.
    await expect.poll(async () => (await goodFrame.boundingBox())?.height ?? 0, { timeout: 15_000 })
      .toBeGreaterThan(300);

    // A throwing script degrades the widget, not the conversation.
    const throwing = frameCard("throws.html");
    await throwing.scrollIntoViewIfNeeded();
    await expect(throwing).toHaveAttribute("data-widget-state", "degraded", { timeout: 30_000 });
    await expect(throwing.getByRole("status")).toContainText("code failed to run");
    // A blocked external script is classified separately from a throw.
    const external = frameCard("external.html");
    await external.scrollIntoViewIfNeeded();
    await expect(external).toHaveAttribute("data-widget-state", "degraded", { timeout: 30_000 });
    await expect(external.getByRole("status")).toContainText("external dependency did not load");
    await expect.poll(() => violations.length, { timeout: 15_000 }).toBeGreaterThan(0);
    expect(violations.join("\n")).toContain("example.com");

    // The console violation above proves *a* policy is in force; the document also
    // carries its own meta policy, so it would still pass if a proxy dropped the
    // header. `frame-ancestors` only exists in the header (a meta element ignores it),
    // so fetching the route directly is what pins the header itself.
    const previewPath = await good.locator("iframe").getAttribute("src");
    const previewResponse = await request.get(previewPath ?? "", { headers: { Origin: origin } });
    expect(previewResponse.ok()).toBeTruthy();
    const csp = previewResponse.headers()["content-security-policy"] ?? "";
    expect(csp).toContain("sandbox allow-scripts");
    expect(csp).toContain("frame-ancestors 'self'");
    expect(csp).toContain("connect-src 'none'");
    expect(csp).not.toContain("allow-same-origin");
    // The run itself, and the rest of the message, are untouched by both failures.
    await expect(page.getByText("published the canvas")).toBeVisible();
    await expect(page.locator('[data-artifact-kind="table"]')).toBeVisible();
  } finally {
    await llm.close();
    // The provider and model are rows in the Worker's config, and the model was made
    // the default: leaving it behind would point the next session at a closed port.
    if (modelId) await request.delete(`/api/worker/v1/model-profiles/${modelId}`, { headers: { Origin: originRef } });
    if (providerId) await request.delete(`/api/worker/v1/provider-profiles/${providerId}`, { headers: { Origin: originRef } });
    // The project row owns the session and the artifacts, and it points at a directory
    // this test is about to delete: leaving it behind leaves a broken row in the Worker.
    if (projectId) await request.delete(`/api/worker/v1/projects/${projectId}`, { headers: { Origin: originRef } });
    if (workspace !== null) rmSync(workspace, { recursive: true, force: true });
  }
});

test("a widget that never reports ready says so and can be retried", async ({ page }) => {
  const project = { id: "canvas-project", name: "Canvas study", description: "", status: "active",
    createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };
  const session = { id: "canvas-session", projectId: project.id, title: "Canvas", status: "active",
    activeLeafMessageId: "m3", activeBranchId: "main",
    createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:03Z" };
  const policies = ["discuss", "ask", "auto"].map(mode => ({
    id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1,
    config: { mode }, status: "active", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z",
  }));
  const widget = { artifactId: "silent", name: "silent.html", kind: "widget",
    mimeType: "text/html; charset=utf-8", sizeBytes: 128, sha256: "silent-sha" };
  const messages = [
    { id: "m1", sessionId: session.id, role: "user", status: "complete", createdAt: "2026-11-11T00:00:01Z",
      parts: [{ type: "text", text: "publish a canvas" }] },
    { id: "m2", sessionId: session.id, parentMessageId: "m1", role: "assistant", status: "complete",
      createdAt: "2026-11-11T00:00:02Z",
      parts: [{ type: "tool_call", toolCall: { id: "publish", name: "publish_artifact", arguments: { path: "/workspace/silent.html", kind: "widget" } } }] },
    { id: "m3", sessionId: session.id, parentMessageId: "m2", role: "tool", status: "complete",
      createdAt: "2026-11-11T00:00:03Z",
      parts: [{ type: "tool_result", toolResult: { toolCallId: "publish", toolName: "publish_artifact",
        content: "published", isError: false, artifacts: [widget] } }] },
  ];
  const fulfill = (route: Route, data: unknown) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });

  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) {
      return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m3" });
    }
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/recovery`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/branches`) {
      return fulfill(route, [{ id: "main", sessionId: session.id, label: "Main", leafMessageId: "m3",
        messageCount: 3, active: true, createdAt: session.createdAt, updatedAt: session.updatedAt }]);
    }
    if (path === `/v1/sessions/${session.id}/artifacts/silent/preview`) {
      // A frame that loads and then says nothing: the one state the real envelope
      // cannot produce, because its receiver always reports.
      return route.fulfill({ status: 200, contentType: "text/html; charset=utf-8",
        body: "<!doctype html><html><body><div style='height:80px'>silent</div></body></html>" });
    }
    return route.fulfill({ status: 599, body: `unmocked ${path}` });
  });

  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByRole("button", { name: session.title, exact: true }).click();
  const widgetSection = page.locator('[data-artifact-kind="widget"]');
  await expect(widgetSection).toBeVisible();
  await expect(widgetSection.getByRole("alert")).toContainText("did not respond", { timeout: 20_000 });
  // The frame stays mounted: a late `ready` on this frame should still rescue it, and
  // `no-store` makes retry reload rather than replay.
  await expect(widgetSection.locator(".artifact-widget")).toHaveAttribute("data-widget-state", "stalled");
  await expect(widgetSection.locator("iframe")).toHaveAttribute("data-widget-attempt", "0");
  await page.getByRole("button", { name: "Retry" }).click();
  // Retry remounts the frame instead of leaving a dead box behind.
  await expect(widgetSection.locator("iframe")).toHaveAttribute("data-widget-attempt", "1");
  await expect(widgetSection.locator(".artifact-widget")).toHaveAttribute("data-widget-state", "loading");
  // The remount starts over: same silence, same honest answer.
  await expect(widgetSection.getByRole("alert")).toContainText("did not respond", { timeout: 20_000 });
});

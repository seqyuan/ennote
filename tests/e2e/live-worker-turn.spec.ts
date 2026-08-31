import { createServer, type Server } from "node:http";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test, type APIRequestContext } from "@playwright/test";
import { selectProject } from "./harness";

test.describe.configure({ mode: "serial" });

function startFakeOpenAI(): Promise<{ baseURL: string; close: () => Promise<void> }> {
  const server: Server = createServer((req, res) => {
    if ((req.url ?? "").includes("/chat/completions")) {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write('data: {"id":"e2e","model":"e2e-echo","choices":[{"delta":{"content":"pong from worker"},"finish_reason":null}]}\n\n');
      res.write('data: {"id":"e2e","model":"e2e-echo","choices":[{"delta":{},"finish_reason":"stop"}]}\n\n');
      res.write("data: [DONE]\n\n");
      res.end();
      return;
    }
    res.writeHead(404);
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
        close: () => new Promise((done) => server.close(() => done())),
      });
    });
  });
}

async function workerData<T>(request: APIRequestContext, origin: string, method: "get" | "post", path: string, body?: unknown): Promise<T> {
  const headers = { Origin: origin, Accept: "application/json" };
  const response = method === "get"
    ? await request.get(`/api/worker${path}`, { headers })
    : await request.post(`/api/worker${path}`, { headers, data: body });
  const json = await response.json() as { data?: T; error?: { message?: string } };
  expect(response.ok(), `${path} ${response.status()}: ${JSON.stringify(json)}`).toBeTruthy();
  return json.data as T;
}

test("submit turn against the live worker streams the assistant reply", async ({ page, request }) => {
  const llm = await startFakeOpenAI();
  try {
    await page.goto("/");
    const origin = new URL(page.url()).origin;
    const stamp = Date.now();
    const provider = await workerData<{ id: string }>(request, origin, "post", "/v1/provider-profiles", {
      name: "E2E Local", key: `e2e-local-${stamp}`, providerType: "openai-compatible",
      baseUrl: llm.baseURL, apiKey: "e2e-key",
    });
    await workerData(request, origin, "post", "/v1/model-profiles", {
      providerId: provider.id, modelName: "e2e-echo", displayName: "E2E Echo",
      contextWindow: 8000, maxOutputTokens: 256, supportsToolUse: true,
      thinkingDialect: "none", supportedThinkingEfforts: ["default"], isDefault: true,
    });
    const workspace = mkdtempSync(join(tmpdir(), "ennote-e2e-ws-"));
    const created = await workerData<{ project: { id: string; name: string } }>(request, origin, "post", "/v1/projects", {
      name: `Live worker ${stamp}`, hostPath: workspace,
    });
    const session = await workerData<{ id: string; title: string }>(
      request, origin, "post", `/v1/projects/${created.project.id}/sessions`,
      { title: `Live turn ${stamp}` },
    );

    await page.goto("/");
    await page.getByTestId("ennote-shell").waitFor();
    await selectProject(page, created.project.name);
    if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
    await page.getByRole("button", { name: session.title, exact: true }).click();

    await page.getByRole("textbox", { name: "Message the agent" }).fill("ping");
    await expect(page.getByRole("button", { name: "Send", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(page.getByText("pong from worker")).toBeVisible({ timeout: 30_000 });
  } finally {
    await llm.close();
  }
});

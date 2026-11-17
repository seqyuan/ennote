import { rmSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { dirname } from "node:path";
import { expect, test, type APIRequestContext } from "@playwright/test";

test.describe.configure({ mode: "serial" });

// Every name, key and id below is suffixed with one stamp, so a repeat run never
// collides with a previous one and cleanup can be scoped to exactly what this
// test created.
const MARKER = "graph-run-e2e";

/** One scripted answer: tool calls, or plain text plus a stop. */
type ScriptedReply = { toolCalls: { name: string; arguments: Record<string, unknown> }[] } | { text: string };

type ScriptedProvider = {
  baseURL: string;
  /** Every chat completion body the Worker sent, in order. */
  requests: Record<string, unknown>[];
  close: () => Promise<void>;
};

/** One SSE chunk in the shape `ennoworker/internal/llm/sse.go` parses. */
function completionFrame(delta: Record<string, unknown>, finishReason: string | null) {
  return { id: "e2e", model: "e2e-echo", choices: [{ delta, finish_reason: finishReason }] };
}

// A tool call is streamed the way a provider streams it: one delta carrying the
// call's index, id, name and whole argument string, then a terminal
// finish_reason. The child Run's only legal terminal contract is a
// `submit_result` tool call (ennoworker/internal/agent/loop.go intercepts it
// before tool dispatch), so a script that never sends one cannot end the Run.
function frames(reply: ScriptedReply) {
  if ("text" in reply) {
    return [completionFrame({ content: reply.text }, null), completionFrame({}, "stop")];
  }
  const toolCalls = reply.toolCalls.map((call, index) => ({
    index,
    id: `call_e2e_${index}`,
    type: "function",
    function: { name: call.name, arguments: JSON.stringify(call.arguments) },
  }));
  return [completionFrame({ tool_calls: toolCalls }, null), completionFrame({}, "tool_calls")];
}

/**
 * A scriptable OpenAI-compatible endpoint. Entry N answers the Nth chat
 * completion; entries past the end answer with plain text so an unexpected extra
 * call reports as a wrong answer instead of hanging the test.
 */
function startScriptedProvider(script: ScriptedReply[]): Promise<ScriptedProvider> {
  const requests: Record<string, unknown>[] = [];
  const server: Server = createServer((req, res) => {
    if (!(req.url ?? "").includes("/chat/completions")) {
      res.writeHead(404);
      res.end();
      return;
    }
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("end", () => {
      const body = Buffer.concat(chunks).toString("utf8");
      try {
        requests.push(JSON.parse(body) as Record<string, unknown>);
      } catch {
        requests.push({ malformedBody: body });
      }
      const reply: ScriptedReply = script[requests.length - 1] ?? { text: "e2e turn complete" };
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      for (const frame of frames(reply)) res.write(`data: ${JSON.stringify(frame)}\n\n`);
      res.write("data: [DONE]\n\n");
      res.end();
    });
  });
  return new Promise((resolve, reject) => {
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        reject(new Error("scripted provider did not bind a port"));
        return;
      }
      resolve({
        baseURL: `http://127.0.0.1:${address.port}/v1`,
        requests,
        close: () => new Promise((done) => server.close(() => done())),
      });
    });
  });
}

type WorkerMethod = "get" | "post" | "put" | "patch" | "delete";

/**
 * One `/api/worker/v1` call against the live Worker behind the gate. `Origin` is
 * the running gate's own origin because the Worker refuses a mutating request
 * that carries no matching Origin.
 */
async function workerApi<T>(
  request: APIRequestContext, origin: string, method: WorkerMethod, path: string, body?: unknown,
): Promise<T> {
  const response = await request.fetch(`/api/worker${path}`, {
    method: method.toUpperCase(),
    headers: { Origin: origin, Accept: "application/json" },
    ...(body === undefined ? {} : { data: body }),
  });
  if (response.status() === 204) return undefined as T;
  const json = await response.json() as { data?: T; error?: { code?: string; message?: string } };
  expect(response.ok(), `${method.toUpperCase()} ${path} ${response.status()}: ${JSON.stringify(json)}`).toBeTruthy();
  return json.data as T;
}

/** Best-effort cleanup: a failed delete must not hide the test's own verdict. */
async function ignoreFailure(action: () => Promise<unknown>): Promise<void> {
  try {
    await action();
  } catch {
    // Cleanup only. The assertion that matters already ran.
  }
}

type RunDetail = {
  run: {
    runId: string;
    state: string;
    flowVersionId: string;
    terminalReason?: string;
    totalTokensUsed: number;
  };
  nodes: {
    handle: string;
    terminalState: string;
    errorCode?: string;
    childRunId?: string;
    outputRef?: Record<string, unknown>;
  }[];
};

const TERMINAL_FLOW_STATES = new Set(["completed", "failed", "cancelled", "convergence_exceeded", "budget_exceeded"]);

function describeRun(detail: RunDetail): string {
  return JSON.stringify({
    run: { state: detail.run.state, terminalReason: detail.run.terminalReason },
    nodes: detail.nodes.map((node) => ({
      handle: node.handle, terminalState: node.terminalState, errorCode: node.errorCode, childRunId: node.childRunId,
    })),
  });
}

test("a Graph Run of a local-Hero Task reaches a terminal verdict on the live Worker", async ({ page, request }) => {
  // The Run itself is the slow part (child Run dispatch, one model call, one
  // submit_result), so the poll gets a bounded minute on top of the API setup.
  test.setTimeout(120_000);
  const stamp = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const graphID = `e2e-graph-${stamp}`;
  const providerKey = `e2e-local-${stamp}`;
  const provider = await startScriptedProvider([{
    toolCalls: [{
      name: "submit_result",
      arguments: {
        status: "completed",
        summary: "The E2E task produced its marker.",
        payload: { marker: MARKER, source: "graph-run.spec" },
      },
    }],
  }]);
  let origin = "";
  let workspace: string | null = null;
  let graphPath: string | null = null;
  let projectID: string | null = null;
  let modelID: string | null = null;
  let providerID: string | null = null;
  try {
    await page.goto("/");
    origin = new URL(page.url()).origin;

    const createdProvider = await workerApi<{ id: string }>(request, origin, "post", "/v1/provider-profiles", {
      name: `E2E Local ${stamp}`, key: providerKey, providerType: "openai-compatible",
      baseUrl: provider.baseURL, apiKey: "e2e-key",
    });
    providerID = createdProvider.id;
    const model = await workerApi<{ id: string }>(request, origin, "post", "/v1/model-profiles", {
      providerId: createdProvider.id, modelName: "e2e-echo", displayName: `E2E Echo ${stamp}`,
      contextWindow: 8000, maxOutputTokens: 256, supportsToolUse: true,
      thinkingDialect: "none", supportedThinkingEfforts: ["default"], isDefault: true,
    });
    modelID = model.id;
    // A Project's workspace has to live inside a root the Worker reports, so it
    // is created through the same endpoint the host picker's "New folder" uses.
    const listing = await workerApi<{ roots: string[] }>(request, origin, "get", "/v1/host/directories");
    workspace = (await workerApi<{ path: string }>(request, origin, "post", "/v1/host/directories", {
      path: listing.roots[0], name: `ennote-e2e-graph-ws-${stamp}`,
    })).path;
    const created = await workerApi<{ project: { id: string } }>(request, origin, "post", "/v1/projects", {
      name: `E2E graph project ${stamp}`, hostPath: workspace,
    });
    projectID = created.project.id;
    // The Session's default model is the one fact the flow anchor freezes into
    // the parent Run's config (store/agent_flow_runs.go), and every delegated
    // task child resolves its own config from that parent, so a Session without
    // one cannot dispatch a child at all.
    const session = await workerApi<{ id: string }>(
      request, origin, "post", `/v1/projects/${projectID}/sessions`,
      { title: `E2E graph session ${stamp}`, defaultModelProfileId: model.id },
    );

    const graph = await workerApi<{ id: string; digest: string; path: string }>(
      request, origin, "post", "/v1/graphs", { id: graphID, name: `E2E graph ${stamp}` },
    );
    graphPath = graph.path;
    // One hero Task, created together with the Graph-local Hero it references:
    // the mutation refuses a local/<handle> that no draft backs, and a private
    // Hero that a Graph Run cannot admit is refused by the publish validator.
    await workerApi(request, origin, "patch", `/v1/graphs/${graphID}`, {
      expectedDigest: graph.digest,
      task: {
        id: "produce",
        value: { hero: "local/e2e-hero", goal: "Produce the E2E marker.", budget: { tokens: 4000 } },
      },
      localHero: {
        schemaVersion: 1, handle: "e2e-hero", name: `E2E Hero ${stamp}`,
        description: "Deterministic Hero for the Graph Run guard", positioning: "",
        icon: "bot", color: "neutral",
        model: { ref: `${providerKey}/e2e-echo`, thinkingEffort: "default", fallbacks: [] },
        skills: [], authority: "read_only", permissionCeiling: "auto", allowedTools: ["read"],
        context: { defaultMode: "task_only", allowedModes: ["task_only"], ownExecutionContinuity: "none" },
        delegation: {
          admission: "auto_within_budget", allowedCallerKinds: ["host"], allowedStrategies: ["single"],
          maxInvocationsPerParentRun: 4, maxConcurrentInstances: 4,
          budgetCeiling: {
            maxModelCalls: 4, maxToolCalls: 4, maxTotalTokens: 20000,
            maxOutputTokens: 2000, maxCostUsdMicros: 0, maxWallTimeMs: 60000,
          },
        },
        outputContract: "text-v1", maxLoopIterations: 4, prompt: "Submit one structured result.",
      },
    });

    const revision = await workerApi<{ version: number; digest: string; heroDigests: Record<string, string> }>(
      request, origin, "post", `/v1/graphs/${graphID}/publish`,
    );
    expect(revision.version).toBe(1);
    expect(revision.digest).toMatch(/^sha256:[0-9a-f]{64}$/);
    // The private Hero is frozen into the revision, not read from the draft at
    // Run start.
    expect(Object.keys(revision.heroDigests)).toEqual(["e2e-hero"]);

    const binding = await workerApi<{ enabled: boolean; revision: string }>(
      request, origin, "put", `/v1/projects/${projectID}/graph-bindings/${graphID}`,
      { enabled: true, revision: `v${String(revision.version).padStart(6, "0")}` },
    );
    expect(binding).toMatchObject({ enabled: true, revision: "v000001" });

    const started = await workerApi<{ runId: string; state: string; flowVersionId: string }>(
      request, origin, "post", `/v1/graphs/${graphID}/runs`, { sessionId: session.id },
    );
    expect(started.runId).toMatch(/^[0-9a-f-]{36}$/);
    expect(started.flowVersionId).toBe(`${graphID}@v000001`);

    const readRun = () => workerApi<RunDetail>(
      request, origin, "get", `/v1/sessions/${session.id}/graph-runs/${started.runId}`,
    );
    let detail = await readRun();
    const deadline = Date.now() + 60_000;
    while (!TERMINAL_FLOW_STATES.has(detail.run.state) && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 500));
      detail = await readRun();
    }

    expect(TERMINAL_FLOW_STATES.has(detail.run.state), `Run did not settle: ${describeRun(detail)}`).toBeTruthy();
    expect(detail.run.state, describeRun(detail)).toBe("completed");
    expect(detail.run.terminalReason ?? "").toBe("");
    expect(detail.nodes).toHaveLength(1);
    const [node] = detail.nodes;
    expect(node.handle).toBe("produce");
    expect(node.terminalState, describeRun(detail)).toBe("completed");
    expect(node.errorCode ?? "").toBe("");
    expect(node.childRunId).toMatch(/^[0-9a-f-]{36}$/);
    // The checkpoint carries the child's own submit_result payload, so the
    // Task's output is the result the Hero reported, not a projection of it.
    expect(node.outputRef, describeRun(detail)).toMatchObject({ marker: MARKER, source: "graph-run.spec" });

    // The Task child is a delegated Run: one model call, one terminal tool call.
    // Anything else means the scripted provider was consulted differently than
    // the delegated contract allows.
    expect(provider.requests).toHaveLength(1);
    const advertised = (provider.requests[0].tools ?? []) as { function?: { name?: string } }[];
    expect(advertised.map((tool) => tool.function?.name)).toContain("submit_result");
  } finally {
    await provider.close();
    if (projectID !== null) await ignoreFailure(() => workerApi(request, origin, "delete", `/v1/projects/${projectID}`));
    if (modelID !== null) await ignoreFailure(() => workerApi(request, origin, "delete", `/v1/model-profiles/${modelID}`));
    if (providerID !== null) await ignoreFailure(() => workerApi(request, origin, "delete", `/v1/provider-profiles/${providerID}`));
    if (graphPath !== null) rmSync(dirname(graphPath), { recursive: true, force: true });
    if (workspace !== null) rmSync(workspace, { recursive: true, force: true });
  }
});

import { execFile, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { afterEach, describe, expect, it } from "vitest";

const root = path.resolve(import.meta.dirname, "../..");
const require = createRequire(import.meta.url);
const execFileAsync = promisify(execFile);

type Endpoint = { url: string; token: string; pid: number; instanceId?: string };

const launcher = require(path.join(root, "bin/ennote.js")) as {
  workerStatePath(home: string): string;
  isLoopbackHostname(hostname: string): boolean;
  loadWorkerEndpoint(home: string): Endpoint;
  requestPackInspect(endpoint: Endpoint, target: string, options?: { documents?: boolean; timeoutMs?: number }): Promise<Record<string, unknown>>;
  formatPackSummary(data: Record<string, unknown>): string;
  requestJSON(endpoint: Endpoint, method: string, pathname: string, options?: { body?: unknown; action?: string; timeoutMs?: number }): Promise<unknown>;
  requestPackInstall(endpoint: Endpoint, target: string, options?: { dryRun?: boolean; renames?: { flows?: Record<string, string>; roles?: Record<string, string> } }): Promise<Record<string, unknown>>;
  parsePackRenames(entries: string[] | undefined, flag: string): Record<string, string>;
  parsePackID(value: string): string;
  formatPackInstallPlan(plan: Record<string, unknown>, options?: { dryRun?: boolean }): string;
  formatPackList(summaries: Array<Record<string, unknown>>): string;
};

const temporaryDirectories: string[] = [];
const servers: http.Server[] = [];

function temporaryHome(): string {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "ennote-pack-"));
  temporaryDirectories.push(home);
  fs.mkdirSync(path.join(home, "runtime"), { recursive: true });
  return home;
}

function writeWorkerState(home: string, state: Record<string, unknown>): string {
  const file = path.join(home, "runtime", "worker-state.json");
  fs.writeFileSync(file, JSON.stringify(state));
  return file;
}

function deadPid(): number {
  const result = spawnSync(process.execPath, ["-e", "process.exit(0)"]);
  return result.pid ?? 2 ** 31 - 1;
}

async function startStubWorker(handler: (request: http.IncomingMessage, body: string) => { status: number; body?: unknown }): Promise<{ server: http.Server; port: number; requests: Array<{ method?: string; url?: string; headers: http.IncomingHttpHeaders; body: string }> }> {
  const requests: Array<{ method?: string; url?: string; headers: http.IncomingHttpHeaders; body: string }> = [];
  const server = http.createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on("data", chunk => chunks.push(chunk as Buffer));
    request.on("end", () => {
      const body = Buffer.concat(chunks).toString("utf8");
      requests.push({ method: request.method, url: request.url, headers: request.headers, body });
      const result = handler(request, body);
      if (result.body === undefined) {
        response.writeHead(result.status);
        response.end();
        return;
      }
      response.writeHead(result.status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(result.body));
    });
  });
  servers.push(server);
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("stub worker has no port");
  return { server, port: address.port, requests };
}

const inspectResult = {
  inspectVersion: 1,
  source: { kind: "path", path: "/home/me/packs/cell-qc" },
  container: { kind: "directory", bytes: 1024 },
  schemaVersion: 1,
  pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
  manifestDigest: `sha256:${"a".repeat(64)}`,
  packDigest: `sha256:${"b".repeat(64)}`,
  signature: { present: false, algorithm: null, path: null, verified: false },
  signed: false,
  flows: [{ id: "qc-review", path: "flows/qc-review/graph.yaml", format: "graph", taskCount: 2, entryTask: "load", roles: ["qc-reviewer"], checkTasks: 0, fanOutTasks: 0, maxTotalTokens: 0, pathDigest: `sha256:${"c".repeat(64)}`, sourceDigest: `sha256:${"d".repeat(64)}` }],
  roles: [{ handle: "qc-reviewer", path: "roles/qc-reviewer/role.md", scope: "flow", name: "QC Reviewer", authority: "read_only", permissionCeiling: "discuss", allowedTools: ["read"], skills: [], pathDigest: `sha256:${"e".repeat(64)}`, sourceDigest: `sha256:${"f".repeat(64)}` }],
  skills: [{ id: "cell-qc-tables", path: "skills/cell-qc-tables", kind: "vendored", pathDigest: `sha256:${"1".repeat(64)}`, promptBytes: 12 }],
  mcp: [{ ref: "pubmed@1" }],
  requires: [],
  capabilityEnvelope: {
    flowCount: 1, roleCount: 1, skillCount: 1, taskCount: 2,
    roles: ["qc-reviewer"], skills: ["cell-qc-tables"], requiresMcp: ["pubmed@1"],
    modelRequirements: { capabilities: ["tool_use"], minContextWindow: 128000, perRole: {} },
    checkCommands: [], tools: ["read"], writes: [], flowMaxTotalTokens: 0,
  },
  files: [{ path: "pack.yaml", bytes: 812, digest: `sha256:${"2".repeat(64)}` }],
  filesTotal: 1,
  filesTruncated: false,
  warnings: [{ code: "unsigned_pack", severity: "warning", message: "pack is unsigned" }],
  manifest: "schema_version: 1\n",
};

afterEach(() => {
  for (const server of servers.splice(0)) server.close();
  for (const directory of temporaryDirectories.splice(0)) fs.rmSync(directory, { recursive: true, force: true });
});

describe("ennote pack inspect", () => {
  it("reads the worker handshake instead of the ennogate state file", () => {
    const home = temporaryHome();
    expect(launcher.workerStatePath(home)).toBe(path.join(home, "runtime", "worker-state.json"));
  });

  it("recognizes only loopback hostnames", () => {
    for (const hostname of ["127.0.0.1", "127.1.2.3", "localhost", "::1", "[::1]"]) {
      expect(launcher.isLoopbackHostname(hostname)).toBe(true);
    }
    for (const hostname of ["10.0.0.5", "example.com", "192.168.1.2", "0.0.0.0", "128.0.0.1"]) {
      expect(launcher.isLoopbackHostname(hostname)).toBe(false);
    }
  });

  it("explains that the Worker is not running when the state file is missing", () => {
    const home = temporaryHome();
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/not running/);
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/ennote start/);
  });

  it("rejects a non-loopback or non-http Worker url", () => {
    const home = temporaryHome();
    writeWorkerState(home, { version: 1, url: "http://10.0.0.5:41234", pid: process.pid, bootstrapToken: "token" });
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/non-loopback/);

    writeWorkerState(home, { version: 1, url: "https://127.0.0.1:41234", pid: process.pid, bootstrapToken: "token" });
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/expected http:/);

    writeWorkerState(home, { version: 1, url: "not a url", pid: process.pid, bootstrapToken: "token" });
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/invalid url/);
  });

  it("rejects a dead worker pid and a missing token", () => {
    const home = temporaryHome();
    writeWorkerState(home, { version: 1, url: "http://127.0.0.1:41234", pid: deadPid(), bootstrapToken: "token" });
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/is not running/);

    writeWorkerState(home, { version: 1, url: "http://127.0.0.1:41234", pid: process.pid, bootstrapToken: "" });
    expect(() => launcher.loadWorkerEndpoint(home)).toThrow(/bootstrap token/);
  });

  it("posts the inspect request with the Worker bearer token", async () => {
    const { port, requests } = await startStubWorker(() => ({ status: 200, body: { data: inspectResult } }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };

    const data = await launcher.requestPackInspect(endpoint, "/home/me/packs/cell-qc", { documents: true });
    expect(data).toEqual(inspectResult);

    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("POST");
    expect(requests[0].url).toBe("/v1/packs/inspect");
    expect(requests[0].headers.authorization).toBe("Bearer secret-token");
    expect(requests[0].headers["content-type"]).toBe("application/json");
    expect(JSON.parse(requests[0].body)).toEqual({
      source: { kind: "path", path: "/home/me/packs/cell-qc" },
      includeDocuments: true,
    });
  });

  it("surfaces Worker error codes", async () => {
    const { port } = await startStubWorker(() => ({
      status: 422,
      body: { error: { code: "pack_manifest_invalid", message: "pack.yaml has an unknown field" } },
    }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };
    await expect(launcher.requestPackInspect(endpoint, "/home/me/packs/bad"))
      .rejects.toThrow(/unknown field \(pack_manifest_invalid\)/);
  });

  it("prints a human summary", () => {
    const summary = launcher.formatPackSummary(inspectResult);
    expect(summary).toContain("Cell QC Review (seqyuan/cell-qc-review@1.3.0)");
    expect(summary).toContain("Signature: Unsigned");
    expect(summary).toContain(`Manifest: ${inspectResult.manifestDigest}`);
    expect(summary).toContain("Flows (1)");
    expect(summary).toContain("MCP (1)");
    expect(summary).toContain("pubmed@1 (external ref, not resolved or executed)");
    expect(summary).toContain("unsigned_pack");
  });

  it("omits the MCP section when a pack declares no MCP refs", () => {
    const summary = launcher.formatPackSummary({ ...inspectResult, mcp: [] });
    expect(summary).not.toContain("MCP (");
  });

  it("prints the raw result with --json against a running stub Worker", async () => {
    const home = temporaryHome();
    const { port } = await startStubWorker(() => ({ status: 200, body: { data: inspectResult } }));
    writeWorkerState(home, {
      version: 1,
      url: `http://127.0.0.1:${port}`,
      pid: process.pid,
      instanceId: "instance",
      bootstrapToken: "secret-token",
      startedAt: new Date().toISOString(),
    });

    // The stub Worker runs in this process, so the CLI must be spawned
    // asynchronously: a blocking spawnSync would deadlock the event loop.
    const { stdout } = await execFileAsync(
      process.execPath,
      [path.join(root, "bin/ennote.js"), "pack", "inspect", "/home/me/packs/cell-qc", "--json"],
      { encoding: "utf8", env: { ...process.env, ENNOTE_HOME: home } },
    );
    expect(JSON.parse(stdout)).toEqual(inspectResult);
  });

  it("fails with a clear message when the service is not running", () => {
    const home = temporaryHome();
    const result = spawnSync(process.execPath, [path.join(root, "bin/ennote.js"), "pack", "inspect", "/home/me/packs/cell-qc"], {
      encoding: "utf8",
      env: { ...process.env, ENNOTE_HOME: home },
    });
    expect(result.status).toBe(1);
    expect(result.stderr).toContain("Ennote Worker is not running");
    expect(result.stderr).toContain("ennote start");
  });

  it("treats a missing pack target as a usage error", () => {
    const home = temporaryHome();
    const result = spawnSync(process.execPath, [path.join(root, "bin/ennote.js"), "pack", "inspect"], {
      encoding: "utf8",
      env: { ...process.env, ENNOTE_HOME: home },
    });
    expect(result.status).toBe(2);
    expect(result.stderr).toContain("Usage: ennote pack inspect");
  });
});

const installPlan = {
  planVersion: 1,
  pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
  manifestDigest: `sha256:${"a".repeat(64)}`,
  packDigest: `sha256:${"b".repeat(64)}`,
  actions: [
    { kind: "graph", storeId: "qc-review", action: "create" },
    { kind: "graph_role", storeId: "qc-review/qc-reviewer", action: "create", scope: "flow" },
    { kind: "skill", storeId: "cell-qc-tables", action: "create", scope: "global" },
  ],
  blockers: [],
  warnings: [{ code: "pack_flow_renamed", severity: "info", message: "flow installed as another Graph" }],
  commitPoint: "state_file",
  willPublishFlowRevision: false,
  state: {
    stateVersion: 1,
    pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
    manifestDigest: `sha256:${"a".repeat(64)}`,
    packDigest: `sha256:${"b".repeat(64)}`,
    signed: false,
    installedAt: "2026-10-27T12:00:00Z",
    installState: "installed_draft",
    source: { kind: "path", path: "/home/me/sources/cell-qc" },
    flows: [{ packFlowId: "qc-review", storeId: "qc-review", format: "graph", sourceDigest: `sha256:${"c".repeat(64)}`, installedGraphDigest: `sha256:${"c".repeat(64)}`, publishedRevision: null }],
    roles: [{ packHandle: "qc-reviewer", scope: "flow", storeId: "qc-review/qc-reviewer", digest: `sha256:${"d".repeat(64)}` }],
    skills: [{ packId: "cell-qc-tables", storeId: "cell-qc-tables", scope: "global", contentDigest: `sha256:${"e".repeat(64)}` }],
    externalRefs: { skills: ["bio-workspace@0.1.0"], mcp: [] },
    modelRequirements: { capabilities: ["tool_use"], minContextWindow: 128000, perRole: {} },
  },
};

describe("ennote pack install", () => {
  it("posts the install body with dryRun and renames", async () => {
    const { port, requests } = await startStubWorker(() => ({ status: 200, body: { data: installPlan } }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };

    const plan = await launcher.requestPackInstall(endpoint, "/home/me/sources/cell-qc", {
      dryRun: true,
      renames: { flows: { "qc-review": "cell-qc-review" }, roles: { "qc-reviewer": "cell-qc-reviewer" } },
    });
    expect(plan).toEqual(installPlan);

    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("POST");
    expect(requests[0].url).toBe("/v1/packs/install");
    expect(requests[0].headers.authorization).toBe("Bearer secret-token");
    expect(requests[0].headers["content-type"]).toBe("application/json");
    expect(JSON.parse(requests[0].body)).toEqual({
      source: { kind: "path", path: "/home/me/sources/cell-qc" },
      dryRun: true,
      renames: { flows: { "qc-review": "cell-qc-review" }, roles: { "qc-reviewer": "cell-qc-reviewer" } },
    });
  });

  it("omits empty dryRun-free renames from the install body", async () => {
    const { port, requests } = await startStubWorker(() => ({ status: 200, body: { data: installPlan } }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };

    await launcher.requestPackInstall(endpoint, "/home/me/sources/cell-qc", { dryRun: false, renames: { flows: {}, roles: {} } });
    expect(JSON.parse(requests[0].body)).toEqual({
      source: { kind: "path", path: "/home/me/sources/cell-qc" },
      dryRun: false,
    });
  });

  it("surfaces the first install blocker code", async () => {
    const { port } = await startStubWorker(() => ({
      status: 422,
      body: { error: { code: "pack_flow_format_unsupported", message: "flow \"qc-review\" uses format: flow" } },
    }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };
    await expect(launcher.requestPackInstall(endpoint, "/home/me/sources/cell-qc"))
      .rejects.toThrow(/format: flow \(pack_flow_format_unsupported\)/);
  });

  it("lists installed packs over GET with the bearer token", async () => {
    const summaries = [{
      pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
      manifestDigest: `sha256:${"a".repeat(64)}`,
      packDigest: `sha256:${"b".repeat(64)}`,
      installedAt: "2026-10-27T12:00:00Z",
      installState: "installed_draft",
      source: { kind: "path", path: "/home/me/sources/cell-qc" },
      flowCount: 1, roleCount: 1, skillCount: 1,
      externalRefs: { skills: [], mcp: [] },
    }];
    const { port, requests } = await startStubWorker(() => ({ status: 200, body: { data: summaries } }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };

    const data = await launcher.requestJSON(endpoint, "GET", "/v1/packs", { action: "pack list" });
    expect(data).toEqual(summaries);
    expect(requests[0].method).toBe("GET");
    expect(requests[0].url).toBe("/v1/packs");
    expect(requests[0].headers.authorization).toBe("Bearer secret-token");
    expect(requests[0].body).toBe("");
  });

  it("removes a pack over DELETE with force", async () => {
    const { port, requests } = await startStubWorker(() => ({ status: 204 }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };

    const data = await launcher.requestJSON(endpoint, "DELETE", "/v1/packs/seqyuan/cell-qc-review?force=1", { action: "pack remove" });
    expect(data).toBeNull();
    expect(requests[0].method).toBe("DELETE");
    expect(requests[0].url).toBe("/v1/packs/seqyuan/cell-qc-review?force=1");
    expect(requests[0].headers.authorization).toBe("Bearer secret-token");
  });

  it("surfaces remove error codes", async () => {
    const { port } = await startStubWorker(() => ({
      status: 409,
      body: { error: { code: "pack_install_blocked", message: "installed objects were edited locally" } },
    }));
    const endpoint = { url: `http://127.0.0.1:${port}`, token: "secret-token", pid: process.pid };
    await expect(launcher.requestJSON(endpoint, "DELETE", "/v1/packs/seqyuan/cell-qc-review", { action: "pack remove" }))
      .rejects.toThrow(/edited locally \(pack_install_blocked\)/);
  });

  it("parses old=new rename flags and rejects malformed ones", () => {
    expect(launcher.parsePackRenames([], "--as-flow")).toEqual({});
    expect(launcher.parsePackRenames(undefined, "--as-role")).toEqual({});
    expect(launcher.parsePackRenames(["qc-review=cell-qc-review", "load=qc-load"], "--as-flow"))
      .toEqual({ "qc-review": "cell-qc-review", load: "qc-load" });
    expect(() => launcher.parsePackRenames(["qc-review"], "--as-flow")).toThrow(/expects old=new/);
    expect(() => launcher.parsePackRenames(["=new"], "--as-flow")).toThrow(/expects old=new/);
    expect(() => launcher.parsePackRenames(["old="], "--as-flow")).toThrow(/expects old=new/);
    expect(() => launcher.parsePackRenames(["old=a", "old=b"], "--as-flow")).toThrow(/maps old twice/);
  });

  it("validates the pack id before calling the Worker", () => {
    expect(launcher.parsePackID("seqyuan/cell-qc-review")).toBe("seqyuan/cell-qc-review");
    for (const value of ["cell-qc-review", "seqyuan/", "/cell-qc", "a/b/c", "seqyuan/.."]) {
      expect(() => launcher.parsePackID(value)).toThrow(/<publisher>\/<name>/);
    }
  });

  it("prints a human install plan with actions, blockers, warnings, and the draft state", () => {
    const summary = launcher.formatPackInstallPlan(installPlan, { dryRun: true });
    expect(summary).toContain("Dry run (nothing written): Cell QC Review (seqyuan/cell-qc-review@1.3.0)");
    expect(summary).toContain("Actions (3): 3 create");
    expect(summary).toContain("create graph qc-review");
    expect(summary).toContain("create graph_role qc-review/qc-reviewer (flow)");
    expect(summary).toContain("State:    installed_draft; flows 1, roles 1, skills 1");
    expect(summary).toContain("Disabled: qc-review (draft Graph, no published revision");
    expect(summary).toContain("Recorded (not installed): skills bio-workspace@0.1.0; mcp none");
    expect(summary).toContain("Blockers (0)");
    expect(summary).toContain("pack_flow_renamed");

    const blocked = launcher.formatPackInstallPlan({
      ...installPlan,
      state: undefined,
      actions: [],
      blockers: [{ code: "pack_flow_format_unsupported", severity: "error", message: "needs S2b" }],
      warnings: [],
    }, { dryRun: true });
    expect(blocked).toContain("State:    not written (dry run)");
    expect(blocked).toContain("Blockers (1)");
    expect(blocked).toContain("pack_flow_format_unsupported (error): needs S2b");
  });

  it("prints installed pack summaries", () => {
    expect(launcher.formatPackList([])).toBe("No Agent Packs installed.");
    const summary = launcher.formatPackList([{
      pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" },
      installState: "installed_draft", flowCount: 1, roleCount: 2, skillCount: 1,
      installedAt: "2026-10-27T12:00:00Z",
    }]);
    expect(summary).toContain("Installed Agent Packs (1)");
    expect(summary).toContain("Cell QC Review (seqyuan/cell-qc-review@1.3.0) installed_draft");
    expect(summary).toContain("flows 1, roles 2, skills 1");
  });
});

describe("ennote pack subcommands against a stub Worker", () => {
  it("installs with --dry-run and renames, printing the raw plan with --json", async () => {
    const home = temporaryHome();
    const { port, requests } = await startStubWorker(() => ({ status: 200, body: { data: installPlan } }));
    writeWorkerState(home, {
      version: 1, url: `http://127.0.0.1:${port}`, pid: process.pid,
      instanceId: "instance", bootstrapToken: "secret-token", startedAt: new Date().toISOString(),
    });

    const { stdout } = await execFileAsync(
      process.execPath,
      [
        path.join(root, "bin/ennote.js"), "pack", "install", "./relative/cell-qc",
        "--dry-run", "--json", "--as-flow", "qc-review=cell-qc-review", "--as-role", "qc-reviewer=cell-qc-reviewer",
      ],
      { encoding: "utf8", cwd: home, env: { ...process.env, ENNOTE_HOME: home } },
    );
    expect(JSON.parse(stdout)).toEqual(installPlan);

    expect(requests).toHaveLength(1);
    expect(requests[0].url).toBe("/v1/packs/install");
    const body = JSON.parse(requests[0].body);
    // The CLI resolves the source path before it goes on the wire.
    expect(body.source.path).toBe(path.join(home, "relative", "cell-qc"));
    expect(body.dryRun).toBe(true);
    expect(body.renames).toEqual({
      flows: { "qc-review": "cell-qc-review" },
      roles: { "qc-reviewer": "cell-qc-reviewer" },
    });
  });

  it("lists packs and removes one with --force", async () => {
    const home = temporaryHome();
    const summaries = [{ pack: { id: "seqyuan/cell-qc-review", name: "Cell QC Review", version: "1.3.0" }, installState: "installed_draft", flowCount: 1, roleCount: 1, skillCount: 1, installedAt: "2026-10-27T12:00:00Z" }];
    const { port, requests } = await startStubWorker((request) => {
      if (request.method === "DELETE") return { status: 204 };
      return { status: 200, body: { data: summaries } };
    });
    writeWorkerState(home, {
      version: 1, url: `http://127.0.0.1:${port}`, pid: process.pid,
      instanceId: "instance", bootstrapToken: "secret-token", startedAt: new Date().toISOString(),
    });

    const listed = await execFileAsync(
      process.execPath,
      [path.join(root, "bin/ennote.js"), "pack", "list", "--json"],
      { encoding: "utf8", env: { ...process.env, ENNOTE_HOME: home } },
    );
    expect(JSON.parse(listed.stdout)).toEqual(summaries);
    expect(requests[0].method).toBe("GET");
    expect(requests[0].url).toBe("/v1/packs");

    const removed = await execFileAsync(
      process.execPath,
      [path.join(root, "bin/ennote.js"), "pack", "remove", "seqyuan/cell-qc-review", "--force"],
      { encoding: "utf8", env: { ...process.env, ENNOTE_HOME: home } },
    );
    expect(removed.stdout.trim()).toBe("Removed seqyuan/cell-qc-review (forced)");
    expect(requests[1].method).toBe("DELETE");
    expect(requests[1].url).toBe("/v1/packs/seqyuan/cell-qc-review?force=1");
  });

  it("reports a missing Worker for install, list, and remove", () => {
    const home = temporaryHome();
    for (const args of [
      ["pack", "install", "/home/me/sources/cell-qc"],
      ["pack", "list"],
      ["pack", "remove", "seqyuan/cell-qc-review"],
    ]) {
      const result = spawnSync(process.execPath, [path.join(root, "bin/ennote.js"), ...args], {
        encoding: "utf8",
        env: { ...process.env, ENNOTE_HOME: home },
      });
      expect(result.status).toBe(1);
      expect(result.stderr).toContain("Ennote Worker is not running");
      expect(result.stderr).toContain("ennote start");
    }
  });

  it("treats missing targets and bad flags as usage errors", () => {
    const home = temporaryHome();
    for (const args of [
      ["pack", "install"],
      ["pack", "remove"],
      ["pack", "install", "/home/me/sources/cell-qc", "--as-flow", "bogus"],
      ["pack", "remove", "not-a-pack-id"],
    ]) {
      const result = spawnSync(process.execPath, [path.join(root, "bin/ennote.js"), ...args], {
        encoding: "utf8",
        env: { ...process.env, ENNOTE_HOME: home },
      });
      expect(result.status).toBe(2);
    }
  });
});

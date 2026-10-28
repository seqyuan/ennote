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

async function startStubWorker(handler: (request: http.IncomingMessage, body: string) => { status: number; body: unknown }): Promise<{ server: http.Server; port: number; requests: Array<{ method?: string; url?: string; headers: http.IncomingHttpHeaders; body: string }> }> {
  const requests: Array<{ method?: string; url?: string; headers: http.IncomingHttpHeaders; body: string }> = [];
  const server = http.createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on("data", chunk => chunks.push(chunk as Buffer));
    request.on("end", () => {
      const body = Buffer.concat(chunks).toString("utf8");
      requests.push({ method: request.method, url: request.url, headers: request.headers, body });
      const result = handler(request, body);
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
  requires: [],
  capabilityEnvelope: {
    flowCount: 1, roleCount: 1, skillCount: 1, taskCount: 2,
    roles: ["qc-reviewer"], skills: ["cell-qc-tables"],
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
    expect(summary).toContain("unsigned_pack");
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

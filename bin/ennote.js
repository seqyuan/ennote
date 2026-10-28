#!/usr/bin/env node
"use strict";

const { spawn } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const readline = require("node:readline");
const { parseArgs } = require("node:util");

const DEFAULT_PORT = "45131";
const DEFAULT_HOSTNAME = "127.0.0.1";
const START_TIMEOUT_MS = 45_000;
const STOP_TIMEOUT_MS = 15_000;
const PACK_INSPECT_TIMEOUT_MS = 15_000;
const GITHUB_RELEASES_BASE = "https://github.com/seqyuan/ennote/releases/download";

function platformSuffix(platform = os.platform(), arch = os.arch()) {
  if (!new Set(["linux", "darwin"]).has(platform)) {
    throw new Error(`Unsupported platform: ${platform}`);
  }
  if (!new Set(["x64", "arm64"]).has(arch)) {
    throw new Error(`Unsupported architecture: ${arch}`);
  }
  return `${platform}-${arch}`;
}

function runtimeBinDir(home) {
  return path.join(home, "bin");
}

function runtimePaths(packageDir, home, version, platform = os.platform(), arch = os.arch()) {
  const suffix = platformSuffix(platform, arch);
  // Check local bin cache first, fallback to npm-installed worker/ directory
  const cachedGate = path.join(runtimeBinDir(home), `ennogate-${version}-${suffix}`);
  const cachedWorker = path.join(runtimeBinDir(home), `ennoworker-${version}-${suffix}`);
  const bundledGate = path.join(packageDir, "worker", `ennogate-${suffix}`);
  const bundledWorker = path.join(packageDir, "worker", `ennoworker-${suffix}`);
  return {
    gate: fs.existsSync(cachedGate) ? cachedGate : bundledGate,
    worker: fs.existsSync(cachedWorker) ? cachedWorker : bundledWorker,
    staticDir: path.join(packageDir, "out"),
  };
}

function statePaths(home) {
  return {
    state: path.join(home, "ennote-state.json"),
    log: path.join(home, "logs", "ennote.log"),
    auth: path.join(home, "config", "auth.json"),
  };
}

// workerStatePath is the owner-only handshake file ennoworker writes and
// ennogate supervises (see internal/runtimeinfo.WorkerState).
function workerStatePath(home) {
  return path.join(home, "runtime", "worker-state.json");
}

function usageError(message) {
  const error = new Error(message);
  error.exitCode = 2;
  return error;
}

function validateRuntime(paths) {
  for (const [name, file] of [["ennogate", paths.gate], ["ennoworker", paths.worker]]) {
    if (!fs.existsSync(file)) throw new Error(`Missing ${name} binary: ${file}`);
  }
  const index = path.join(paths.staticDir, "index.html");
  if (!fs.existsSync(index)) throw new Error(`Missing static frontend: ${index}`);
}

function ensureDirectory(directory) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
}

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

function writeJSONAtomic(file, value) {
  ensureDirectory(path.dirname(file));
  const temporary = `${file}.tmp-${process.pid}-${crypto.randomBytes(4).toString("hex")}`;
  fs.writeFileSync(temporary, JSON.stringify(value), { mode: 0o600 });
  fs.renameSync(temporary, file);
}

function removeFile(file) {
  try { fs.unlinkSync(file); } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }
}

function isPidAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function healthCheck(url, timeoutMs = 2_000) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const request = http.get(`${url}/api/health`, (response) => {
      let body = "";
      response.setEncoding("utf8");
      response.on("data", (chunk) => { body += chunk; });
      response.on("end", () => {
        if (response.statusCode !== 200) return finish(false);
        try {
          finish(JSON.parse(body).status === "ok");
        } catch {
          finish(false);
        }
      });
    });
    request.once("error", () => finish(false));
    request.setTimeout(timeoutMs, () => {
      request.destroy();
      finish(false);
    });
  });
}

function canBind(port, hostname) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.unref();
    server.once("error", () => resolve(false));
    server.listen(Number(port), hostname, () => server.close(() => resolve(true)));
  });
}

async function waitForExit(pid, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (isPidAlive(pid) && Date.now() < deadline) await sleep(100);
  return !isPidAlive(pid);
}

function serviceURL(hostname, port) {
  const displayHost = hostname === "0.0.0.0" ? "127.0.0.1" : hostname;
  const urlHost = displayHost.includes(":") && !displayHost.startsWith("[") ? `[${displayHost}]` : displayHost;
  return `http://${urlHost}:${port}`;
}

function createLogger(logFile, quiet) {
  ensureDirectory(path.dirname(logFile));
  return (message) => {
    const line = `[${new Date().toISOString()}] ${message}`;
    fs.appendFileSync(logFile, `${line}\n`, { mode: 0o600 });
    if (!quiet) console.error(line);
  };
}

function requireHTTPS(url) {
  if (!String(url).startsWith("https://")) {
    throw new Error(`refusing non-HTTPS download: ${url}`);
  }
}

function downloadFile(url, destPath) {
  return new Promise((resolve, reject) => {
    try { requireHTTPS(url); } catch (error) { return reject(error); }
    const file = fs.createWriteStream(destPath, { mode: 0o500 });
    const request = https.get(url, (response) => {
      if (response.statusCode === 302 || response.statusCode === 301 || response.statusCode === 307 || response.statusCode === 308) {
        file.close();
        fs.unlinkSync(destPath);
        return downloadFile(response.headers.location, destPath).then(resolve, reject);
      }
      if (response.statusCode !== 200) {
        file.close();
        fs.unlinkSync(destPath);
        return reject(new Error(`HTTP ${response.statusCode} for ${url}`));
      }
      response.pipe(file);
      file.once("finish", () => {
        file.close();
        fs.chmodSync(destPath, 0o700);
        resolve();
      });
    });
    request.once("error", (error) => {
      file.close();
      try { fs.unlinkSync(destPath); } catch {}
      reject(error);
    });
    request.setTimeout(120_000, () => {
      request.destroy();
      file.close();
      try { fs.unlinkSync(destPath); } catch {}
      reject(new Error(`Download timed out: ${url}`));
    });
  });
}

function parseSHA256SUMS(text, filename) {
  const needle = filename.replace(/\\/g, "/");
  for (const line of String(text).split(/\r?\n/)) {
    const match = line.match(/^([a-fA-F0-9]{64})\s+\*?(\S+)\s*$/);
    if (!match) continue;
    const listed = match[2].replace(/^\.\//, "").split("/").pop();
    if (listed === needle) return match[1].toLowerCase();
  }
  throw new Error(`SHA256SUMS has no entry for ${filename}`);
}

function fileSHA256(filePath) {
  return crypto.createHash("sha256").update(fs.readFileSync(filePath)).digest("hex");
}

function verifyFileSHA256(filePath, expected) {
  const actual = fileSHA256(filePath);
  if (actual !== expected.toLowerCase()) {
    throw new Error(`checksum mismatch for ${path.basename(filePath)}`);
  }
}

function downloadText(url) {
  return new Promise((resolve, reject) => {
    try { requireHTTPS(url); } catch (error) { return reject(error); }
    const request = https.get(url, (response) => {
      if (response.statusCode === 302 || response.statusCode === 301 || response.statusCode === 307 || response.statusCode === 308) {
        return downloadText(response.headers.location).then(resolve, reject);
      }
      if (response.statusCode !== 200) {
        return reject(new Error(`HTTP ${response.statusCode} for ${url}`));
      }
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    });
    request.once("error", reject);
    request.setTimeout(60_000, () => {
      request.destroy();
      reject(new Error(`Download timed out: ${url}`));
    });
  });
}

async function ensureBinaries(home, pkg, log, platform = os.platform(), arch = os.arch()) {
  const suffix = platformSuffix(platform, arch);
  const version = pkg.version;
  const binDir = runtimeBinDir(home);
  ensureDirectory(binDir);
  const gatePath = path.join(binDir, `ennogate-${version}-${suffix}`);
  const workerPath = path.join(binDir, `ennoworker-${version}-${suffix}`);
  const needed = [];
  if (!fs.existsSync(gatePath)) needed.push({ name: "ennogate", path: gatePath });
  if (!fs.existsSync(workerPath)) needed.push({ name: "ennoworker", path: workerPath });
  if (needed.length === 0) return;
  log(`Downloading ${needed.length} binary(s) for ${suffix}...`);
  const sumsUrl = `${GITHUB_RELEASES_BASE}/v${version}/SHA256SUMS`;
  const sumsText = await downloadText(sumsUrl);
  for (const { name, path: dest } of needed) {
    const filename = `${name}-${suffix}`;
    const expected = parseSHA256SUMS(sumsText, filename);
    const url = `${GITHUB_RELEASES_BASE}/v${version}/${filename}`;
    await downloadFile(url, dest);
    try {
      verifyFileSHA256(dest, expected);
    } catch (error) {
      try { fs.unlinkSync(dest); } catch {}
      throw error;
    }
  }
  log("Binaries ready.");
}

async function startService(options) {
  const { home, packageDir, port, hostname, json } = options;
  const files = statePaths(home);
  const url = serviceURL(hostname, port);
  const log = createLogger(files.log, json);
  const existing = readJSON(files.state);
  if (existing && isPidAlive(existing.pid)) {
    const healthy = await healthCheck(existing.url ?? url);
    if (healthy) return { alreadyRunning: true, pid: existing.pid, url: existing.url ?? url };
    throw new Error(`ennogate process ${existing.pid} is alive but unhealthy`);
  }
  if (existing) removeFile(files.state);
  if (!await canBind(port, hostname)) throw new Error(`Port ${port} is already in use on ${hostname}`);

  const pkg = JSON.parse(fs.readFileSync(path.join(packageDir, "package.json"), "utf8"));
  await ensureBinaries(home, pkg, log);
  const runtime = runtimePaths(packageDir, home, pkg.version);
  validateRuntime(runtime);
  ensureDirectory(home);
  const output = fs.openSync(files.log, "a", 0o600);
  const child = spawn(runtime.gate, [], {
    detached: true,
    env: {
      ...process.env,
      ENNOTE_HOME: home,
      ENNOTE_STATIC_DIR: runtime.staticDir,
      ENNOTE_WORKER_PATH: runtime.worker,
      ...(fs.existsSync(path.join(packageDir, "skills"))
        ? { ENNOTE_BUILTIN_SKILLS_DIR: path.join(packageDir, "skills") }
        : {}),
      PORT: String(port),
      ENNOTE_HOSTNAME: hostname,
    },
    stdio: ["ignore", output, output],
  });
  child.unref();
  fs.closeSync(output);
  writeJSONAtomic(files.state, {
    version: 1,
    pid: child.pid,
    port: String(port),
    hostname,
    url,
    startedAt: new Date().toISOString(),
  });
  log(`starting ennogate pid=${child.pid} url=${url}`);

  const deadline = Date.now() + START_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (!isPidAlive(child.pid)) {
      removeFile(files.state);
      throw new Error("ennogate exited before becoming ready; inspect ennote logs");
    }
    if (await healthCheck(url)) return { alreadyRunning: false, pid: child.pid, url };
    await sleep(150);
  }
  try { process.kill(child.pid, "SIGTERM"); } catch {}
  if (!await waitForExit(child.pid, 3_000)) {
    try { process.kill(child.pid, "SIGKILL"); } catch {}
  }
  removeFile(files.state);
  throw new Error(`ennogate did not become ready within ${START_TIMEOUT_MS / 1000}s`);
}

async function stopService(home) {
  const files = statePaths(home);
  const state = readJSON(files.state);
  if (!state) return { stopped: false };
  if (!isPidAlive(state.pid)) {
    removeFile(files.state);
    return { stopped: false, stale: true };
  }
  try { process.kill(state.pid, "SIGTERM"); } catch {}
  if (!await waitForExit(state.pid, STOP_TIMEOUT_MS)) {
    try { process.kill(state.pid, "SIGKILL"); } catch {}
    await waitForExit(state.pid, 2_000);
  }
  removeFile(files.state);
  return { stopped: true };
}

async function serviceStatus(home) {
  const state = readJSON(statePaths(home).state);
  if (!state || !isPidAlive(state.pid)) {
    return { running: false, stale: Boolean(state) };
  }
  const healthy = await healthCheck(state.url);
  return { running: healthy, healthy, pid: state.pid, url: state.url, port: state.port };
}

function isLoopbackHostname(hostname) {
  const name = String(hostname ?? "").replace(/^\[|\]$/g, "").toLowerCase();
  if (name === "localhost" || name === "::1") return true;
  const match = name.match(/^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  return Boolean(match) && match.slice(1).every(part => Number(part) <= 255);
}

// loadWorkerEndpoint reads the Worker handshake written by ennoworker and
// refuses anything that is not a live loopback http Worker: ennote pack inspect
// talks to the real Worker instead of re-implementing pack parsing in Node.
function loadWorkerEndpoint(home) {
  const file = workerStatePath(home);
  if (!fs.existsSync(file)) {
    throw new Error(`Ennote Worker is not running (${file} is missing). Start it with: ennote start`);
  }
  let state;
  try {
    state = JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    throw new Error(`Ennote Worker state ${file} is unreadable. Restart it with: ennote restart`);
  }
  let parsed;
  try {
    parsed = new URL(String(state?.url ?? ""));
  } catch {
    throw new Error(`Ennote Worker state ${file} has an invalid url: ${state?.url ?? ""}`);
  }
  if (parsed.protocol !== "http:") {
    throw new Error(`refusing Worker url ${state.url}: expected http:`);
  }
  if (!isLoopbackHostname(parsed.hostname)) {
    throw new Error(`refusing non-loopback Worker url ${state.url}`);
  }
  if (!isPidAlive(state.pid)) {
    throw new Error(`Ennote Worker pid ${state.pid} is not running. Start it with: ennote start`);
  }
  if (typeof state.bootstrapToken !== "string" || state.bootstrapToken === "") {
    throw new Error(`Ennote Worker state ${file} has no bootstrap token. Restart it with: ennote restart`);
  }
  return { url: parsed.origin, token: state.bootstrapToken, pid: state.pid, instanceId: state.instanceId };
}

function postJSON(url, body, headers, timeoutMs) {
  return new Promise((resolve, reject) => {
    const payload = Buffer.from(JSON.stringify(body), "utf8");
    const request = http.request(url, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Content-Length": payload.length, ...headers },
    }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve({
        statusCode: response.statusCode ?? 0,
        body: Buffer.concat(chunks).toString("utf8"),
      }));
    });
    request.once("error", reject);
    request.setTimeout(timeoutMs, () => {
      request.destroy(new Error(`Worker request timed out after ${timeoutMs / 1000}s`));
    });
    request.end(payload);
  });
}

async function requestPackInspect(endpoint, target, options = {}) {
  const body = {
    source: { kind: "path", path: target },
    includeDocuments: Boolean(options.documents),
  };
  if (options.expectedDigest) body.expectedDigest = options.expectedDigest;
  const response = await postJSON(
    `${endpoint.url}/v1/packs/inspect`,
    body,
    { Authorization: `Bearer ${endpoint.token}` },
    options.timeoutMs ?? PACK_INSPECT_TIMEOUT_MS,
  );
  if (response.statusCode < 200 || response.statusCode >= 300) {
    let message = `Worker returned HTTP ${response.statusCode}`;
    let code;
    try {
      const parsed = JSON.parse(response.body);
      code = parsed?.error?.code;
      if (parsed?.error?.message) message = parsed.error.message;
    } catch {
      // A non-JSON error body keeps the status-only message.
    }
    const error = new Error(code ? `${message} (${code})` : message);
    error.code = code;
    error.statusCode = response.statusCode;
    throw error;
  }
  let parsed;
  try {
    parsed = JSON.parse(response.body);
  } catch {
    throw new Error("Worker returned an unreadable inspect response");
  }
  if (!parsed || typeof parsed !== "object" || parsed.data == null) {
    throw new Error("Worker returned an unexpected inspect response");
  }
  return parsed.data;
}

function formatPackSummary(data) {
  const pack = data.pack ?? {};
  const container = data.container ?? {};
  const signature = data.signature ?? {};
  const lines = [];
  lines.push(`${pack.name ?? "(unnamed pack)"} (${pack.id ?? "?"}@${pack.version ?? "?"})`);
  if (pack.description) lines.push(pack.description);
  lines.push(`Source:   ${data.source?.path ?? "?"} (${container.kind ?? "?"}, ${container.bytes ?? 0} bytes)`);
  lines.push(signature.present
    ? `Signature: ${signature.algorithm} at ${signature.path} — UNVERIFIED (inspect never verifies signatures)`
    : "Signature: Unsigned");
  lines.push(`Manifest: ${data.manifestDigest ?? "?"}`);
  lines.push(`Pack:     ${data.packDigest ?? "?"}`);

  const flows = data.flows ?? [];
  lines.push(`Flows (${flows.length})`);
  for (const flow of flows) {
    lines.push(`  - ${flow.id} [${flow.format}] ${flow.taskCount} task(s), ${flow.checkTasks} check, ${flow.fanOutTasks} fan-out, ${flow.maxTotalTokens} tokens`);
  }
  const roles = data.roles ?? [];
  lines.push(`Roles (${roles.length})`);
  for (const role of roles) {
    lines.push(`  - ${role.handle} (${role.scope}) ${role.authority}, tools: ${(role.allowedTools ?? []).join(", ") || "none"}`);
  }
  const skills = data.skills ?? [];
  lines.push(`Skills (${skills.length})`);
  for (const skill of skills) {
    lines.push(`  - ${skill.kind === "external" ? skill.ref : skill.id} (${skill.kind})`);
  }
  const envelope = data.capabilityEnvelope ?? {};
  lines.push(`Checks:   ${(envelope.checkCommands ?? []).join(", ") || "none"}`);
  lines.push(`Writes:   ${(envelope.writes ?? []).join(", ") || "none"}`);
  lines.push(`Files:    ${data.filesTotal ?? 0}${data.filesTruncated ? " (truncated)" : ""}`);

  const warnings = data.warnings ?? [];
  lines.push(`Warnings (${warnings.length})`);
  for (const warning of warnings) lines.push(`  - ${warning.code} (${warning.severity}): ${warning.message}`);
  return lines.join("\n");
}

async function packCommand(home, positionals, values) {
  const action = positionals[1];
  const target = positionals[2];
  if (action !== "inspect") {
    throw usageError(`Unknown pack command: ${action ?? ""}\nUsage: ennote pack inspect <file|dir> [--json] [--documents]`);
  }
  if (!target) {
    throw usageError("Usage: ennote pack inspect <file|dir> [--json] [--documents]");
  }
  const endpoint = loadWorkerEndpoint(home);
  const data = await requestPackInspect(endpoint, path.resolve(target), {
    documents: values.documents,
  });
  if (values.json) console.log(JSON.stringify(data));
  else console.log(formatPackSummary(data));
}

async function setPassword(home, reset) {
  const authFile = statePaths(home).auth;
  ensureDirectory(path.dirname(authFile));
  if (reset) {
    removeFile(authFile);
    return { reset: true };
  }
  if (!process.stdin.isTTY) throw new Error("Password input requires an interactive terminal");
  const prompt = readline.createInterface({ input: process.stdin, output: process.stderr, terminal: true });
  const ask = (question) => new Promise((resolve) => prompt.question(question, resolve));
  try {
    const password = await ask("Enter password: ");
    if (password.length < 4) throw new Error("Password must be at least 4 characters");
    const confirmation = await ask("Confirm password: ");
    if (password !== confirmation) throw new Error("Passwords do not match");
    const bcrypt = require("bcryptjs");
    writeJSONAtomic(authFile, { hash: bcrypt.hashSync(password, 10) });
    return { reset: false };
  } finally {
    prompt.close();
  }
}

function printHelp(version) {
  console.log(`ennote v${version}
Usage: ennote [command] [options]

Commands:
  start           Start the ennogate service
  stop            Stop ennogate and its managed ennoworker
  restart         Restart the service
  status          Show service health
  logs            Show recent service logs
  passwd          Set the local password
  passwd --reset  Remove the local password and require setup
  pack inspect    Inspect an Agent Pack (.enpack or directory) without installing

Options:
  --port, -p      Browser-facing port (default ${DEFAULT_PORT})
  --hostname, -H  Bind address (default ${DEFAULT_HOSTNAME})
  --json          Machine-readable output (pack inspect prints the raw result)
  --documents     pack inspect: include flow, role, and skill source text
  --version, -v   Show version
  --help, -h      Show this help`);
}

async function main(argv = process.argv.slice(2)) {
  const packageDir = path.join(__dirname, "..");
  const pkg = JSON.parse(fs.readFileSync(path.join(packageDir, "package.json"), "utf8"));
  const { values, positionals } = parseArgs({
    args: argv,
    options: {
      port: { type: "string", short: "p", default: DEFAULT_PORT },
      hostname: { type: "string", short: "H", default: DEFAULT_HOSTNAME },
      json: { type: "boolean", default: false },
      documents: { type: "boolean", default: false },
      reset: { type: "boolean", default: false },
      version: { type: "boolean", short: "v", default: false },
      help: { type: "boolean", short: "h", default: false },
    },
    strict: true,
    allowPositionals: true,
  });
  if (values.help) return printHelp(pkg.version);
  if (values.version) return console.log(pkg.version);
  const home = process.env.ENNOTE_HOME ?? path.join(os.homedir(), ".ennote");
  const command = positionals[0] ?? "start";
  let result;
  switch (command) {
    case "start":
      result = await startService({ home, packageDir, port: values.port, hostname: values.hostname, json: values.json });
      break;
    case "stop":
      result = await stopService(home);
      break;
    case "restart":
      await stopService(home);
      result = await startService({ home, packageDir, port: values.port, hostname: values.hostname, json: values.json });
      break;
    case "status":
      result = await serviceStatus(home);
      if (!result.running) process.exitCode = 1;
      break;
    case "logs": {
      const logFile = statePaths(home).log;
      const lines = fs.existsSync(logFile) ? fs.readFileSync(logFile, "utf8").split("\n").filter(Boolean).slice(-100) : [];
      console.log(lines.join("\n"));
      return;
    }
    case "passwd":
      result = await setPassword(home, values.reset);
      break;
    case "pack":
      // pack inspect talks to the running Worker instead of re-implementing
      // manifest parsing and archive safety in Node.
      await packCommand(home, positionals, values);
      return;
    default:
      throw new Error(`Unknown command: ${command}`);
  }
  if (values.json) console.log(JSON.stringify(result));
  else if (command === "start" || command === "restart") console.log(result.url);
  else if (command === "status") console.log(result.running ? `ennote is running at ${result.url}` : "ennote is not running");
  else if (command === "stop") console.log(result.stopped ? "ennote stopped" : "ennote was not running");
  else if (command === "passwd") console.log(result.reset ? "password removed" : "password updated");
}

module.exports = {
  platformSuffix,
  runtimeBinDir,
  runtimePaths,
  statePaths,
  workerStatePath,
  serviceURL,
  validateRuntime,
  healthCheck,
  isPidAlive,
  parseSHA256SUMS,
  fileSHA256,
  verifyFileSHA256,
  ensureBinaries,
  startService,
  stopService,
  serviceStatus,
  setPassword,
  isLoopbackHostname,
  loadWorkerEndpoint,
  requestPackInspect,
  formatPackSummary,
  packCommand,
  main,
};

if (require.main === module) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = Number.isInteger(error?.exitCode) ? error.exitCode : 1;
  });
}

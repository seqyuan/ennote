import { spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const temporary = await mkdtemp(resolve(tmpdir(), "ennote-installed-smoke-"));
const packageOutput = resolve(temporary, "package");
const installRoot = resolve(temporary, "install");
const home = resolve(temporary, "home");
let activeCLI;

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? root,
    env: options.env ?? process.env,
    encoding: "utf8",
    timeout: options.timeout ?? 180_000,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const expected = options.expectedStatus ?? 0;
  if (result.status !== expected) {
    throw new Error([
      `${command} ${args.join(" ")} exited with ${result.status}; expected ${expected}`,
      result.stdout,
      result.stderr,
      result.error?.message,
    ].filter(Boolean).join("\n"));
  }
  return result;
}

function parseLastJSON(output) {
  const lines = output.trim().split("\n").filter(Boolean);
  return JSON.parse(lines.at(-1));
}

function cli(args, options = {}) {
  return run(process.execPath, [activeCLI, ...args], {
    env: { ...process.env, ENNOTE_HOME: home, ENNOTE_SANDBOX: "none", ...options.env },
    expectedStatus: options.expectedStatus,
    timeout: options.timeout ?? 90_000,
  });
}

async function freePort() {
  return await new Promise((resolvePort, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close((error) => error ? reject(error) : resolvePort(address.port));
    });
  });
}

async function request(url, path, options = {}) {
  const headers = new Headers(options.headers);
  if (options.body && !headers.has("content-type")) headers.set("content-type", "application/json");
  const response = await fetch(`${url}${path}`, {
    method: options.method ?? "GET",
    headers,
    body: options.body,
    redirect: "manual",
  });
  const text = await response.text();
  let json;
  try { json = JSON.parse(text); } catch { json = null; }
  return { response, text, json };
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function waitForDead(pid) {
  const deadline = Date.now() + 5_000;
  while (pidAlive(pid) && Date.now() < deadline) {
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  if (pidAlive(pid)) throw new Error(`managed process ${pid} remained alive after stop`);
}

try {
  await mkdir(packageOutput, { recursive: true });
  const pack = run("npm", ["pack", "--json", "--ignore-scripts", "--pack-destination", packageOutput]);
  const report = JSON.parse(pack.stdout)[0];
  const tarball = resolve(packageOutput, report.filename);
  run("npm", ["install", "--prefix", installRoot, "--ignore-scripts", "--omit=dev", "--no-audit", "--no-fund", tarball], {
    timeout: 300_000,
  });

  const installedPackage = resolve(installRoot, "node_modules", "@seqyuan", "ennote");
  activeCLI = resolve(installedPackage, "bin", "ennote.js");
  const sourcePath = resolve(installedPackage, "ennoworker");
  try {
    await stat(sourcePath);
    throw new Error("installed package unexpectedly contains ennoworker source");
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }
  const version = run(process.execPath, [activeCLI, "--version"]).stdout.trim();
  const installedPkg = JSON.parse(await readFile(resolve(installedPackage, "package.json"), "utf8"));
  const expectedVersion = installedPkg.version;
  if (version !== expectedVersion) throw new Error(`unexpected installed version: ${version}, expected ${expectedVersion}`);

  // Pre-populate binary cache if smoke test provides pre-built binaries
  const smokeWorkerDir = process.env.ENNOTE_SMOKE_WORKER_DIR;
  if (smokeWorkerDir) {
    const { mkdir, copyFile } = await import("node:fs/promises");
    const suffix = installedPkg.version;
    await mkdir(resolve(home, "bin"), { recursive: true });
    for (const name of ["ennogate", "ennoworker"]) {
      for (const arch of ["linux-x64", "darwin-arm64", "darwin-x64", "linux-arm64"]) {
        const src = resolve(smokeWorkerDir, `${name}-${arch}`);
        const dest = resolve(home, "bin", `${name}-${suffix}-${arch}`);
        try { await copyFile(src, dest); fs.chmodSync(dest, 0o700); } catch { /* ignore missing platform */ }
      }
    }
  }

  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const started = parseLastJSON(cli(["start", "--port", String(port), "--hostname", "127.0.0.1", "--json"]).stdout);
  if (!started.pid || started.url !== url) throw new Error("launcher returned invalid start state");

  const gateHealth = await request(url, "/api/health");
  if (gateHealth.response.status !== 200 || gateHealth.json?.status !== "ok") throw new Error("ennogate health failed");
  const setupRedirect = await request(url, "/");
  if (setupRedirect.response.status !== 303 || setupRedirect.response.headers.get("location") !== "/setup") {
    throw new Error("first navigation did not require setup");
  }
  const blockedWorker = await request(url, "/api/worker/v1/health/ready");
  if (blockedWorker.response.status !== 428 || blockedWorker.json?.error?.code !== "setup_required") {
    throw new Error("Worker proxy was not blocked before setup");
  }

  const setup = await request(url, "/api/auth/setup", {
    method: "POST",
    headers: { Origin: url },
    body: JSON.stringify({ password: "smoke-password" }),
  });
  if (setup.response.status !== 200) throw new Error(`password setup failed: ${setup.text}`);
  const wrongLogin = await request(url, "/api/auth/login", {
    method: "POST", headers: { Origin: url }, body: JSON.stringify({ password: "wrong-password" }),
  });
  if (wrongLogin.response.status !== 401) throw new Error("wrong password was not rejected");
  const login = await request(url, "/api/auth/login", {
    method: "POST", headers: { Origin: url }, body: JSON.stringify({ password: "smoke-password" }),
  });
  if (login.response.status !== 200) throw new Error(`login failed: ${login.text}`);
  const cookie = login.response.headers.get("set-cookie")?.split(";", 1)[0];
  if (!cookie) throw new Error("login did not return a session cookie");

  const app = await request(url, "/", { headers: { Cookie: cookie } });
  if (app.response.status !== 200 || !app.text.includes("ennote-shell")) throw new Error("static Ennote application was not served");
  const workerReady = await request(url, "/api/worker/v1/health/ready", { headers: { Cookie: cookie } });
  if (workerReady.response.status !== 200 || workerReady.json?.data?.status !== "ready" || workerReady.json?.data?.degraded !== true) {
    throw new Error(`proxied Worker readiness failed: ${workerReady.text}`);
  }
  // Storage layout v2: ennote.db was replaced by catalog.db + usage.db.
  const database = await stat(resolve(home, "data", "catalog.db"));
  if (database.size === 0) throw new Error("startup did not create a migrated database");

  const launcherState = JSON.parse(await readFile(resolve(home, "ennote-state.json"), "utf8"));
  const workerState = JSON.parse(await readFile(resolve(home, "runtime", "worker-state.json"), "utf8"));
  if (!pidAlive(launcherState.pid) || !pidAlive(workerState.pid)) throw new Error("managed processes are not alive");
  const logText = await readFile(resolve(home, "logs", "ennote.log"), "utf8");
  if (logText.includes(workerState.bootstrapToken)) throw new Error("bootstrap token leaked into logs");

  process.kill(launcherState.pid, "SIGKILL");
  await waitForDead(launcherState.pid);
  if (!pidAlive(workerState.pid)) throw new Error("ennoworker did not survive an ennogate crash");
  const recoveredGate = parseLastJSON(cli(["start", "--port", String(port), "--json"]).stdout);
  const recoveredWorker = JSON.parse(await readFile(resolve(home, "runtime", "worker-state.json"), "utf8"));
  if (recoveredWorker.pid !== workerState.pid || recoveredWorker.instanceId !== workerState.instanceId) {
    throw new Error("ennogate did not reconnect to the authenticated surviving ennoworker");
  }

  const conflictHome = resolve(temporary, "conflict-home");
  const blocker = createServer();
  await new Promise((resolveListen, reject) => {
    blocker.once("error", reject);
    blocker.listen(0, "127.0.0.1", resolveListen);
  });
  const conflictPort = blocker.address().port;
  const conflict = run(process.execPath, [activeCLI, "start", "--port", String(conflictPort), "--json"], {
    env: { ...process.env, ENNOTE_HOME: conflictHome, ENNOTE_SANDBOX: "none" },
    expectedStatus: 1,
  });
  if (!conflict.stderr.includes("already in use")) throw new Error("port conflict did not return a clear error");
  blocker.close();

  const stopped = parseLastJSON(cli(["stop", "--json"]).stdout);
  if (!stopped.stopped) throw new Error("launcher did not report a stopped service");
  await waitForDead(recoveredGate.pid);
  await waitForDead(workerState.pid);

  await writeFile(resolve(home, "ennote-state.json"), JSON.stringify({ version: 1, pid: 99_999_999, url }), { mode: 0o600 });
  const restarted = parseLastJSON(cli(["start", "--port", String(port), "--json"]).stdout);
  if (!restarted.pid || restarted.pid === recoveredGate.pid) throw new Error("stale state was not replaced on restart");
  const secondWorkerState = JSON.parse(await readFile(resolve(home, "runtime", "worker-state.json"), "utf8"));
  parseLastJSON(cli(["stop", "--json"]).stdout);
  await waitForDead(restarted.pid);
  await waitForDead(secondWorkerState.pid);

  const finalStatus = parseLastJSON(cli(["status", "--json"], { expectedStatus: 1 }).stdout);
  if (finalStatus.running) throw new Error("stopped service still reports running");
  console.log(`installed package smoke passed: ${report.filename}, port ${port}`);
} finally {
  if (activeCLI) {
    try { cli(["stop", "--json"], { expectedStatus: 0, timeout: 20_000 }); } catch {}
  }
  if (process.env.ENNOTE_KEEP_SMOKE === "1") console.log(`kept smoke directory: ${temporary}`);
  else await rm(temporary, { recursive: true, force: true });
}

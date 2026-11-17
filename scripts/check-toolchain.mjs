#!/usr/bin/env node
/**
 * Toolchain preflight.
 *
 * The default non-interactive shell on a stock host ran Node 18, where Vitest
 * fails while loading its own config with `ERR_REQUIRE_ESM`, and had no `go` on
 * PATH at all. Both surface as "the tests are broken" rather than "the environment
 * is wrong", so this prints the version it found, the version it needs, and the
 * symptom the reader is actually looking at.
 *
 * `--node` is cheap and runs before every `npm test`. `--go` shells out to the Go
 * toolchain and runs only where Go is actually needed.
 */
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const failures = [];

function fail(message) {
  failures.push(message);
}

/**
 * Parses "22", "v22.1.0", "go1.25.0" and "22.22.0" into a comparable pair. The
 * minor defaults to 0 so a range written as ">=22" and a version written as
 * "22.22.0" are comparable.
 */
function versionOf(text) {
  const match = /(?:^go|^v)?(\d+)(?:\.(\d+))?/.exec(text.trim());
  return match === null ? null : { major: Number(match[1]), minor: Number(match[2] ?? 0) };
}

function compare(left, right) {
  if (left.major !== right.major) return left.major - right.major;
  return left.minor - right.minor;
}

function checkNode() {
  const pkg = JSON.parse(readFileSync(path.join(root, "package.json"), "utf8"));
  const range = pkg.engines?.node;
  if (!range) {
    fail("package.json declares no engines.node, so nothing pins the Node the tests need.");
    return;
  }
  const floor = versionOf((/>=?\s*\S+/.exec(range) ?? [])[0] ?? "");
  if (floor === null) {
    fail(`cannot read a minimum version out of engines.node "${range}".`);
    return;
  }
  const actual = versionOf(process.versions.node);
  if (actual === null || compare(actual, floor) < 0) {
    const nvmrc = readFileSync(path.join(root, ".nvmrc"), "utf8").trim();
    fail([
      `Node ${process.versions.node} is below the required ${range} (.nvmrc says ${nvmrc}).`,
      "Under Node 18 vitest fails while loading its config with ERR_REQUIRE_ESM,",
      "which is an environment failure, not a test failure. Run `nvm use` first.",
    ].join("\n  "));
  }
}

function checkGo() {
  const modulePath = path.join(root, "ennoworker");
  let goMod;
  try {
    goMod = readFileSync(path.join(modulePath, "go.mod"), "utf8");
  } catch {
    fail("ennoworker/go.mod is missing; run this from the repository root.");
    return;
  }
  const required = versionOf((/^go\s+(\S+)/m.exec(goMod) ?? [])[1] ?? "");
  if (required === null) {
    fail("cannot read the go directive out of ennoworker/go.mod.");
    return;
  }

  let effective;
  try {
    // `go env GOVERSION` reports the toolchain that will actually run the build.
    // With GOTOOLCHAIN=auto that can be newer than the installed `go version`,
    // which is why the installed version is not what this compares.
    effective = execFileSync("go", ["env", "GOVERSION"], { cwd: modulePath, encoding: "utf8" }).trim();
  } catch {
    fail([
      "go was not found on PATH.",
      `ennoworker/go.mod requires go ${required.major}.${required.minor}.`,
      "Install Go (or set GOTOOLCHAIN=auto with network access) before `make test-go`.",
    ].join("\n  "));
    return;
  }
  const actual = versionOf(effective);
  if (actual === null || compare(actual, required) < 0) {
    fail([
      `the Go toolchain for this module is ${effective}, below the go ${required.major}.${required.minor} ennoworker/go.mod requires.`,
      "Set GOTOOLCHAIN=auto so the directive is honoured, or upgrade Go.",
    ].join("\n  "));
  }
}

if (args.includes("--node")) checkNode();
if (args.includes("--go")) checkGo();
if (!args.includes("--node") && !args.includes("--go")) {
  checkNode();
  checkGo();
}

if (failures.length > 0) {
  console.error("✗ Toolchain:\n  " + failures.join("\n  "));
  process.exit(1);
}

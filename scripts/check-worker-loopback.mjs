#!/usr/bin/env node
/**
 * Loopback-only guard for the Worker authority (AGENTS.md "Authority and
 * Runtime": `ennoworker` must remain loopback-only).
 *
 * `ennoworker` never exposes itself. LAN Web is a product mode of `ennogate`,
 * where `resolveGateAccess` requires ENNOTE_ALLOW_LAN=1 plus a TLS key pair
 * (or an explicit plaintext acknowledgment) before any non-loopback bind.
 *
 * A blanket grep for "0.0.0.0" would miss the real regression and fail on
 * legitimate code, so the rule is four narrow checks:
 *
 *   1. internal/config keeps the Worker listener on 127.0.0.1.
 *   2. No wildcard or empty-host bind literal in ennoworker Go sources.
 *   3. Every listener call site is listed in ALLOWED_LISTENERS with the reason
 *      it is safe, so a new listener needs review instead of silently widening
 *      the listening surface.
 *   4. ENNOTE_HOSTNAME belongs to the gate, and the gate keeps its LAN policy.
 *
 * `*_test.go` files are skipped on purpose: they bind 0.0.0.0 to exercise the
 * gate's Host guard.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = process.cwd();
const CONFIG_FILE = "ennoworker/internal/config/config.go";
const GATE_FILE = "ennoworker/cmd/ennogate/main.go";

// The two listeners in the tree today. A new call site fails the guard until
// it is reviewed and gets an entry here with the reason it cannot be reached
// from outside loopback.
const ALLOWED_LISTENERS = new Map([
  ["ennoworker/cmd/ennoworker/main_v2.go", "listens on cfg.ListenAddr, which internal/config builds from 127.0.0.1"],
  ["ennoworker/cmd/ennogate/main.go", "LAN bind is gated by resolveGateAccess (ENNOTE_ALLOW_LAN plus TLS)"],
]);

// `"0.0.0.0"`, `"0.0.0.0:8080"`, `"::"`, `"[::]:8080"`.
const WILDCARD_BIND = /"(?:0\.0\.0\.0|\[::\]|::)(?::[^"]*)?"/;
// `":8080"` — net.Listen treats an empty host as "every interface". Only
// checked in files that open a listener, because URL builders such as
// webfetch.go legitimately contain a `":443"` suffix.
const EMPTY_HOST_BIND = /(?:^|[^A-Za-z0-9_])":(?:\d+|%s[^"]*)"/;
const LISTENER_CALL = /\bnet\.Listen(?:TCP|Packet)?\(|\bhttp\.ListenAndServe(?:TLS)?\(/;

function goFiles(directory) {
  const files = [];
  for (const entry of readdirSync(directory)) {
    const path = join(directory, entry);
    if (statSync(path).isDirectory()) {
      files.push(...goFiles(path));
    } else if (entry.endsWith(".go") && !entry.endsWith("_test.go")) {
      files.push(path);
    }
  }
  return files;
}

function read(relativePath) {
  try {
    return readFileSync(join(ROOT, relativePath), "utf8");
  } catch {
    return null;
  }
}

const errors = [];

// 1. The Worker's own listener stays on loopback.
const configSource = read(CONFIG_FILE);
const listenAddrField = configSource
  ?.split("\n")
  .find((line) => /^\s*ListenAddr:/.test(line));
if (configSource === null) {
  errors.push(`${CONFIG_FILE} is missing; the Worker listen address cannot be verified`);
} else if (listenAddrField === undefined) {
  errors.push(`${CONFIG_FILE} no longer assigns ListenAddr; keep the Worker listener on 127.0.0.1`);
} else if (!listenAddrField.includes("127.0.0.1")) {
  errors.push(`${CONFIG_FILE}: ListenAddr must be built from 127.0.0.1, found: ${listenAddrField.trim()}`);
}

// 2 and 3. No wildcard bind literals, and no unreviewed listener call sites.
const listenerCounts = new Map();
for (const file of goFiles(join(ROOT, "ennoworker"))) {
  const relativePath = relative(ROOT, file).split("\\").join("/");
  const source = readFileSync(file, "utf8");
  const listenerLines = source.split("\n").filter((line) => LISTENER_CALL.test(line)).length;
  if (listenerLines > 0) listenerCounts.set(relativePath, listenerLines);

  source.split("\n").forEach((line, index) => {
    const where = `${relativePath}:${index + 1}`;
    const wildcard = line.match(WILDCARD_BIND);
    if (wildcard) {
      errors.push(`${where} contains ${wildcard[0]}; the Worker may only listen on loopback (LAN access is ennogate's job)`);
    }
    const emptyHost = listenerLines > 0 ? line.match(EMPTY_HOST_BIND) : null;
    if (emptyHost) {
      errors.push(`${where} passes ${emptyHost[0]} to a listener; an empty host listens on every interface`);
    }
  });

  if (source.includes("ENNOTE_HOSTNAME") && !relativePath.startsWith("ennoworker/cmd/ennogate/")) {
    errors.push(`${relativePath} reads ENNOTE_HOSTNAME; only ennogate may choose a bind host`);
  }
}

// 3. Listener call sites need a review note.
for (const [file, count] of listenerCounts) {
  if (!ALLOWED_LISTENERS.has(file)) {
    errors.push(`${file} opens ${count} listener(s) without a review note; add it to ALLOWED_LISTENERS in scripts/check-worker-loopback.mjs with the reason it stays loopback-only`);
  } else if (count > 1) {
    errors.push(`${file} opens ${count} listeners but only one gated listener is reviewed`);
  }
}
for (const file of ALLOWED_LISTENERS.keys()) {
  if (!listenerCounts.has(file)) {
    errors.push(`${file} no longer opens a listener; drop its ALLOWED_LISTENERS entry`);
  }
}

// 4. The gate keeps the LAN policy that makes a non-loopback bind safe.
const gateSource = read(GATE_FILE);
if (gateSource === null) {
  errors.push(`${GATE_FILE} is missing; the LAN bind policy cannot be verified`);
} else {
  for (const required of ["func resolveGateAccess(", "ENNOTE_ALLOW_LAN"]) {
    if (!gateSource.includes(required)) {
      errors.push(`${GATE_FILE} no longer contains ${JSON.stringify(required)}; the LAN bind policy is what makes a non-loopback listener safe`);
    }
  }
}

if (errors.length > 0) {
  console.error("Loopback-only guard failed:\n");
  for (const error of errors) console.error(`  - ${error}`);
  console.error(`\n${errors.length} issue(s).`);
  process.exit(1);
}

console.log("Loopback-only guard passed.");

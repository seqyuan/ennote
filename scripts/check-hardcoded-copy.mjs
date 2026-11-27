#!/usr/bin/env node
/**
 * Hardcoded-copy guard.
 *
 * A user-visible English string written straight into JSX is invisible to every
 * other check this repository has: `tsc` is happy, the messages-parity test only
 * compares dictionaries to each other, and the i18n coverage test only asks
 * whether the keys a component *does* request exist. The result is a translated
 * shell around panels that still answer in English.
 *
 * The rule: user-visible copy comes from `t("…")`. This guard counts the strings
 * that do not, per file, against a recorded ceiling — the same ratchet shape
 * `scripts/check-file-sizes.mjs` uses, so the number can only go down. Migrating
 * a panel means wrapping its strings and lowering its ceiling in
 * `scripts/hardcoded-copy-allowlist.json`; a file that reaches zero drops out of
 * the list.
 *
 * Usage:
 *   node scripts/check-hardcoded-copy.mjs           # check (fails over a ceiling)
 *   node scripts/check-hardcoded-copy.mjs --list    # print what it counts
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const ALLOWLIST_PATH = "scripts/hardcoded-copy-allowlist.json";
const ROOTS = ["components", "app"];

/** Names that are product names, not words to translate. */
const PROPER_NOUNS = new Set([
  "Skills", "Graph", "Graphs", "Hero", "Heroes", "Chat", "MCP", "Task", "Tasks",
  "Packs", "Builder", "Claude Code", "Codex", "Cursor", "pi", "Ennote", "Session",
  "Sessions", "Project", "Projects", "Run", "Runs", "Worker", "API", "JSON",
]);

/** `>` + copy + `<` on one line: a JSX text node, not an expression. */
const TEXT_NODE = />\s*([A-Z][a-zA-Z0-9 ',.:—–-]{2,})\s*</g;
/** A quote-valued attribute that a reader hears or sees. */
const ATTRIBUTE = /(?:aria-label|title|placeholder|alt)="([A-Z][^"]{2,})"/g;

/** Lines that look like code rather than copy: type members, signatures, generics. */
function isCodeLine(line) {
  if (/^\s*(?:\/\/|\*|\/\*)/.test(line)) return true;
  if (/\bPromise\s*</.test(line)) return true;
  if (/=>\s*(?:void|Promise)/.test(line)) return true;
  if (/^\s*[a-zA-Z]+:\s*\(/.test(line)) return true;
  if (/<[A-Z][a-zA-Z]*\s*\/?>/.test(line) === false && /:\s*[A-Z]/.test(line)) return false;
  return false;
}

function looksLikeCopy(text) {
  if (PROPER_NOUNS.has(text.trim())) return false;
  if (!/[a-z]/.test(text)) return false;
  // A path, a token, or an identifier with no space is usually data, not copy.
  if (/^[\w./:-]+$/.test(text.trim()) && !text.includes(" ")) return false;
  return true;
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (name === "node_modules" || name === "__snapshots__") continue;
      walk(path, out);
    } else if (name.endsWith(".tsx")) {
      out.push(path);
    }
  }
  return out;
}

function countFile(path) {
  const hits = [];
  const lines = readFileSync(path, "utf8").split("\n");
  lines.forEach((line, index) => {
    if (/t\(\s*"/.test(line)) return;
    if (isCodeLine(line)) return;
    for (const match of line.matchAll(TEXT_NODE)) {
      if (looksLikeCopy(match[1])) hits.push({ line: index + 1, kind: "text", copy: match[1] });
    }
    for (const match of line.matchAll(ATTRIBUTE)) {
      if (looksLikeCopy(match[1])) hits.push({ line: index + 1, kind: "attr", copy: match[1] });
    }
  });
  return hits;
}

function main() {
  const listOnly = process.argv.includes("--list");
  const allowlist = JSON.parse(readFileSync(ALLOWLIST_PATH, "utf8"));
  const found = new Map();
  for (const root of ROOTS) {
    for (const file of walk(root)) {
      const hits = countFile(file);
      if (hits.length > 0) found.set(file, hits);
    }
  }

  if (listOnly) {
    for (const [file, hits] of [...found].sort((a, b) => b[1].length - a[1].length)) {
      console.log(`\n${file}  (${hits.length}, ceiling ${allowlist[file] ?? 0})`);
      for (const hit of hits) console.log(`  ${hit.line}  ${hit.kind}  ${hit.copy}`);
    }
    return 0;
  }

  const problems = [];
  for (const [file, hits] of found) {
    const ceiling = allowlist[file] ?? 0;
    if (hits.length > ceiling) {
      problems.push(`${file}: ${hits.length} hardcoded strings, ceiling ${ceiling}`);
      for (const hit of hits.slice(0, 5)) problems.push(`    ${hit.line}  ${hit.copy}`);
    }
  }
  for (const file of Object.keys(allowlist)) {
    if (!found.has(file)) {
      problems.push(`${file}: recorded ceiling ${allowlist[file]} but no hardcoded copy left — delete the entry`);
    } else if (found.get(file).length < allowlist[file]) {
      problems.push(`${file}: ${found.get(file).length} left, ceiling still ${allowlist[file]} — lower it to ${found.get(file).length}`);
    }
  }

  if (problems.length > 0) {
    console.error("Hardcoded copy guard failed:\n");
    for (const line of problems) console.error("  - " + line);
    console.error("\nWrap the copy in t(\"namespace.key\"), add the key to both locales, then lower the ceiling.");
    return 1;
  }

  const files = [...found].filter(([file]) => allowlist[file] !== undefined);
  const total = files.reduce((sum, [, hits]) => sum + hits.length, 0);
  console.log(`Hardcoded copy guard passed (${files.length} files, ${total} recorded strings).`);
  return 0;
}

process.exit(main());

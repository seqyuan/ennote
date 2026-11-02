#!/usr/bin/env node
/**
 * Hot-path test pairing guard (AGENTS.md "Commit and Release Conventions").
 *
 * The message-timeline projection and its controller are a streaming hot path:
 * `tests/unit/chat-messages.test.ts` pins the reference-preservation semantics
 * with `toBe`. Editing the projection or the controller without that test in
 * the same change means the semantics are no longer pinned, so the paths are
 * paired here.
 *
 * Usage:
 *   node scripts/check-chat-messages-pairing.mjs [--base=<rev>]
 *
 * The base is taken from --base, then ENNOTE_PAIRING_BASE (set by CI), then
 * origin/main, then HEAD~1. Committed changes against that base and pending
 * working-tree changes are both inspected, so a local run catches an edit
 * before it is committed.
 *
 * Without a usable base (single-commit clone, no origin/main, CI without a
 * comparison sha) the check prints a hint and exits 0 instead of guessing.
 */

import { spawnSync } from "node:child_process";

const WATCHED = ["lib/chat-messages.ts", "hooks/useChatController.ts"];
const REQUIRED_TESTS = ["tests/unit/chat-messages.test.ts"];

function git(args) {
  return spawnSync("git", args, { encoding: "utf8" });
}

function revExists(rev) {
  return git(["rev-parse", "--verify", "--quiet", `${rev}^{commit}`]).status === 0;
}

function collect(base, files) {
  for (const args of [
    ["diff", "--name-only", `${base}...HEAD`],
    ["diff", "--name-only", base, "HEAD"],
  ]) {
    const result = git(args);
    if (result.status === 0) {
      for (const path of result.stdout.split("\n").filter(Boolean)) files.add(path);
      return true;
    }
  }
  return false;
}

const cliBase = process.argv
  .slice(2)
  .find((argument) => argument.startsWith("--base="))
  ?.slice("--base=".length);
const requested = [cliBase, process.env.ENNOTE_PAIRING_BASE].filter(Boolean);
const candidates = [...requested, "origin/main", "HEAD~1"].filter((rev) => !/^0+$/.test(rev));

const base = candidates.find(revExists);
if (base === undefined) {
  console.log(
    "Chat-messages pairing guard skipped: no comparison base available.\n" +
      `  Watch: ${WATCHED.join(", ")} must change with ${REQUIRED_TESTS.join(", ")}.\n` +
      "  Run locally with: node scripts/check-chat-messages-pairing.mjs --base=origin/main",
  );
  process.exit(0);
}

const changed = new Set();
if (!collect(base, changed)) {
  console.log(`Chat-messages pairing guard skipped: cannot diff ${base} against HEAD.`);
  process.exit(0);
}
const worktree = git(["diff", "--name-only", "HEAD"]);
if (worktree.status === 0) {
  for (const path of worktree.stdout.split("\n").filter(Boolean)) changed.add(path);
}

const touchedHotPath = WATCHED.filter((path) => changed.has(path));
const touchedTest = REQUIRED_TESTS.some((path) => changed.has(path));

if (touchedHotPath.length > 0 && !touchedTest) {
  console.error("Chat-messages pairing guard failed:\n");
  for (const path of touchedHotPath) {
    console.error(`  - ${path} changed against ${base} without ${REQUIRED_TESTS.join(", ")}`);
  }
  console.error(
    "\nThe projection preserves object identity and memoized output; keep the unit test " +
      "that pins those semantics in the same change.",
  );
  process.exit(1);
}

console.log(
  `Chat-messages pairing guard passed (base ${base}, ${touchedHotPath.length} hot-path file(s) changed).`,
);

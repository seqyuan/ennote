#!/usr/bin/env node
/**
 * Repository-hygiene guard (AGENTS.md "Repository Hygiene").
 *
 * .gitignore keeps local-only design history and release artifacts out of new
 * commits, but it cannot stop a deliberate or accidental `git add -f`, so the
 * tracked set is asserted here.
 *
 * The version parity check exists because release.yml only compares the tag
 * against package.json: a stale package-lock.json version would publish an npm
 * package whose metadata disagrees with its own tag.
 */

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";

const UNTRACKED_ONLY = [
  { paths: ["docs", "doc"], reason: "local-only design/history directories" },
  { paths: ["worker"], reason: "a worker bundle built at release time" },
  { paths: ["out"], reason: "a Next.js static export built at release time" },
  { paths: ["ennoworker/ennogate", "ennoworker/ennoworker"], reason: "compiled Go binaries" },
];

function git(args, cwd) {
  return spawnSync("git", args, { cwd, encoding: "utf8" });
}

const errors = [];

const topLevel = git(["rev-parse", "--show-toplevel"]);
const ROOT = topLevel.status === 0 ? topLevel.stdout.trim() : process.cwd();

for (const entry of UNTRACKED_ONLY) {
  const result = git(["ls-files", "--", ...entry.paths], ROOT);
  if (result.status !== 0) {
    console.error(`git ls-files failed: ${result.stderr.trim()}`);
    process.exit(1);
  }
  for (const path of result.stdout.split("\n").filter(Boolean)) {
    errors.push(`${path} is tracked but is ${entry.reason}; remove it with: git rm --cached -- ${path}`);
  }
}

const pkg = JSON.parse(readFileSync(`${ROOT}/package.json`, "utf8"));
const lock = JSON.parse(readFileSync(`${ROOT}/package-lock.json`, "utf8"));
const lockVersions = [lock.version, lock.packages?.[""]?.version].filter(
  (version) => typeof version === "string",
);
if (lockVersions.length === 0) {
  errors.push("package-lock.json has no version field to compare against package.json");
}
for (const version of lockVersions) {
  if (version !== pkg.version) {
    errors.push(`package-lock.json says ${version} but package.json says ${pkg.version}; run: npm install --package-lock-only`);
  }
}

if (errors.length > 0) {
  console.error("Repository hygiene guard failed:\n");
  for (const error of errors) console.error(`  - ${error}`);
  console.error(`\n${errors.length} issue(s).`);
  process.exit(1);
}

console.log("Repository hygiene guard passed.");

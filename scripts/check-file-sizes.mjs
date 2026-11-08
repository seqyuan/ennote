#!/usr/bin/env node
/**
 * File-size budget guard.
 *
 * `check:appshell-size` protected one file with a hand-picked 500-line ceiling.
 * That made the guard a target instead of a signal: AppShell sat at 492/500, so
 * the next feature forced a mechanical extraction, while every other file was
 * free to grow without comment.
 *
 * This guard gives each layer a budget and records an explicit ceiling for the
 * files that legitimately exceed it. A recorded ceiling is a ratchet: the file
 * may shrink, it may not grow, and the list can only get shorter. That keeps the
 * guard aimed at drift — which is what a size limit is actually useful for —
 * instead of at a single magic number.
 */
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

const root = process.cwd();

/**
 * Per-layer budgets. The Go engine has files that are legitimately large
 * (delegation.go, runs.go), so its default only has to catch a new file growing
 * without anyone noticing; the front end is where the churn is.
 */
export const BUDGETS = [
  { prefix: "components/", budget: 500 },
  { prefix: "hooks/", budget: 500 },
  { prefix: "app/", budget: 500 },
  { prefix: "lib/", budget: 600 },
  { prefix: "scripts/", budget: 500 },
  { prefix: "ennoworker/cmd/", budget: 900 },
  { prefix: "ennoworker/internal/", budget: 1200 },
];

/**
 * Files already over their layer budget, with the ceiling recorded when this
 * list was last touched. Lower a ceiling when the file shrinks; delete the entry
 * once the file fits its budget.
 */
export const RECORDED_CEILINGS = {
  "lib/messages.ts": 1100, // flat dictionary, one block per locale
  "hooks/useAgentSession.ts": 700,
  "lib/session-store.ts": 600,
  "lib/chat-messages.ts": 600,
  "components/Composer.tsx": 550,
  "ennoworker/cmd/ennoworker/main.go": 2200,
  "ennoworker/cmd/ennogate/main.go": 1050,
  "ennoworker/internal/store/delegation.go": 2650,
  "ennoworker/internal/store/runs.go": 1580, // FindSubmission + turn Skills encode/decode
  "ennoworker/internal/store/approvals.go": 750,
  "ennoworker/internal/store/run_config_files.go": 740, // turn Skills loader + freeze merge
  "ennoworker/internal/store/mcp_profiles.go": 700,
  "ennoworker/internal/store/agent_flow_runs.go": 680,
  "ennoworker/internal/store/delegation_retry.go": 650,
  "ennoworker/internal/store/compactions.go": 620,
  "ennoworker/internal/tools/webfetch.go": 620,
  "ennoworker/internal/skills/snapshot.go": 800,
  "ennoworker/internal/skillsmgmt/exec.go": 560,
  "ennoworker/internal/skillsmgmt/service.go": 560,
  "ennoworker/internal/api/server.go": 1735, // turn `skills` decode + replay fast path + target-kind guard
  "ennoworker/internal/agentflow/orchestrator.go": 1570,
  "ennoworker/internal/packinstall/plan.go": 1300,
  "ennoworker/internal/agent/loop.go": 1320,
};

/** Generated or vendored files: not hand-written, so no budget applies. */
const EXCLUDED = [/\.gen\.ts$/, /(^|\/)node_modules\//, /(^|\/)out\//, /\.d\.ts$/];

const EXTENSIONS = new Set([".ts", ".tsx", ".mjs", ".js", ".go"]);

function budgetFor(file) {
  // Longest matching prefix wins, so a nested root can override its parent.
  const matches = BUDGETS.filter((entry) => file.startsWith(entry.prefix));
  return matches.length === 0 ? null : matches.sort((a, b) => b.prefix.length - a.prefix.length)[0].budget;
}

async function collect(directory, found = []) {
  let entries;
  try {
    entries = await readdir(path.join(root, directory), { withFileTypes: true });
  } catch {
    return found;
  }
  for (const entry of entries) {
    const relative = path.posix.join(directory, entry.name);
    if (EXCLUDED.some((pattern) => pattern.test(relative))) continue;
    if (entry.isDirectory()) {
      await collect(relative, found);
      continue;
    }
    if (!EXTENSIONS.has(path.extname(entry.name)) || entry.name.includes("_test.")) continue;
    found.push(relative);
  }
  return found;
}

async function lineCount(relative) {
  const contents = await readFile(path.join(root, relative), "utf8");
  if (contents === "") return 0;
  // Count lines the way an editor does: a trailing newline does not open a line.
  const lines = contents.split("\n");
  return lines.at(-1) === "" ? lines.length - 1 : lines.length;
}

const roots = [...new Set(BUDGETS.map((entry) => entry.prefix.split("/")[0]))];
const files = (await Promise.all(roots.map((directory) => collect(directory)))).flat().sort();

const violations = [];
const stale = [];

for (const file of files) {
  const budget = budgetFor(file);
  if (budget === null) continue;
  const lines = await lineCount(file);
  const ceiling = RECORDED_CEILINGS[file];
  if (ceiling === undefined) {
    if (lines > budget) {
      violations.push(`${file}: ${lines} lines exceeds the ${budget}-line budget for its layer`);
    }
    continue;
  }
  if (lines > ceiling) {
    violations.push(`${file}: ${lines} lines exceeds its recorded ceiling of ${ceiling}`);
  } else if (lines < Math.floor(ceiling * 0.75)) {
    stale.push(`${file}: ${lines} lines, recorded ceiling ${ceiling} — lower or remove it`);
  }
}

for (const file of Object.keys(RECORDED_CEILINGS)) {
  if (!files.includes(file)) violations.push(`${file}: recorded in RECORDED_CEILINGS but not found`);
}

if (violations.length > 0) {
  console.error(`File-size guard failed:\n  ${violations.join("\n  ")}`);
  console.error("\nSplit the file, or raise its recorded ceiling in scripts/check-file-sizes.mjs with a reason.");
  process.exit(1);
}

if (stale.length > 0) {
  console.warn(`File-size guard: recorded ceilings have room to tighten:\n  ${stale.join("\n  ")}`);
}

console.log(`File-size guard passed (${files.length} files, ${Object.keys(RECORDED_CEILINGS).length} recorded ceilings).`);

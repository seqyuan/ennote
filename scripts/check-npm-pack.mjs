import { spawnSync } from "node:child_process";

const targets = ["linux-x64", "linux-arm64", "darwin-x64", "darwin-arm64"];
const required = new Set([
  "README.md",
  "bin/ennote.js",
  "out/index.html",
  "package.json",
  ...targets.flatMap((target) => [
    `worker/ennogate-${target}`,
    `worker/ennoworker-${target}`,
  ]),
]);
const allowedRoots = new Set(["README.md", "LICENSE", "bin", "out", "worker", "package.json"]);
const forbidden = [
  /(^|\/)\.env(?:\.|$)/i,
  /\.db(?:-|$)/i,
  /\.log$/i,
  /\.map$/i,
  /(^|\/)tests?\//i,
  /(^|\/)ennoworker\//i,
  /(^|\/)docs\//i,
  /(^|\/)\.next\//i,
  /credential/i,
];

const result = spawnSync("npm", ["pack", "--dry-run", "--json", "--ignore-scripts"], {
  encoding: "utf8",
  stdio: ["ignore", "pipe", "pipe"],
});
if (result.status !== 0) {
  process.stderr.write(result.stderr);
  process.exit(result.status ?? 1);
}
let reports;
try {
  reports = JSON.parse(result.stdout);
} catch (error) {
  throw new Error(`npm pack returned invalid JSON: ${error instanceof Error ? error.message : error}`);
}
if (!Array.isArray(reports) || reports.length !== 1 || !Array.isArray(reports[0].files)) {
  throw new Error("npm pack returned an unexpected report");
}
const files = reports[0].files.map((entry) => entry.path);
const fileSet = new Set(files);
for (const expected of required) {
  if (!fileSet.has(expected)) throw new Error(`npm package is missing required runtime file: ${expected}`);
}
for (const file of files) {
  const root = file.split("/", 1)[0];
  if (!allowedRoots.has(root)) throw new Error(`npm package contains an unexpected root: ${file}`);
  for (const pattern of forbidden) {
    if (pattern.test(file)) throw new Error(`npm package contains forbidden file: ${file}`);
  }
}
for (const target of targets) {
  for (const executable of [`worker/ennogate-${target}`, `worker/ennoworker-${target}`]) {
    const entry = reports[0].files.find((candidate) => candidate.path === executable);
    if ((entry.mode & 0o777) !== 0o755) throw new Error(`runtime binary is not executable: ${executable}`);
  }
}
console.log(`npm package layout verified: ${files.length} files, ${reports[0].unpackedSize} bytes unpacked`);

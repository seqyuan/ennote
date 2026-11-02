import { chmod, mkdir, readdir, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const workerDir = resolve(root, "worker");
const goDir = resolve(root, "ennoworker");
const packageVersion = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8")).version;
// Stamp the release version into the binary so /v1/runtime and every frozen Run
// snapshot name the build that produced them instead of a placeholder.
const ldflags = `-s -w -X github.com/seqyuan/ennote/ennoworker/internal/buildinfo.Version=${packageVersion}`;
const targets = [
  { platform: "linux", nodeArch: "x64", goArch: "amd64" },
  { platform: "linux", nodeArch: "arm64", goArch: "arm64" },
  { platform: "darwin", nodeArch: "x64", goArch: "amd64" },
  { platform: "darwin", nodeArch: "arm64", goArch: "arm64" },
];

await mkdir(workerDir, { recursive: true });
for (const target of targets) {
  for (const command of ["ennogate", "ennoworker"]) {
    const output = resolve(workerDir, `${command}-${target.platform}-${target.nodeArch}`);
    const result = spawnSync("go", ["build", `-ldflags=${ldflags}`, "-trimpath", "-o", output, `./cmd/${command}`], {
      cwd: goDir,
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: target.platform,
        GOARCH: target.goArch,
      },
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    if (result.status !== 0) {
      process.stderr.write(result.stdout ?? "");
      process.stderr.write(result.stderr ?? "");
      throw new Error(`Failed to build ${command} for ${target.platform}-${target.nodeArch}`);
    }
    await chmod(output, 0o755);
    console.log(`built worker/${command}-${target.platform}-${target.nodeArch}`);
  }
}

const names = (await readdir(workerDir)).filter((name) => /^(ennogate|ennoworker)-/.test(name)).sort();
const lines = names.map((name) => `${createHash("sha256").update(readFileSync(resolve(workerDir, name))).digest("hex")}  ${name}`);
await writeFile(resolve(workerDir, "SHA256SUMS"), `${lines.join("\n")}\n`);
console.log("wrote worker/SHA256SUMS");

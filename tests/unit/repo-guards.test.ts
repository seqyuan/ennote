import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

const root = resolve(import.meta.dirname, "../..");
const temporaryDirectories: string[] = [];

function runGuard(script: string, args: string[], cwd: string = root) {
  return spawnSync(process.execPath, [resolve(root, "scripts", script), ...args], {
    cwd,
    encoding: "utf8",
  });
}

function temporaryDirectory(prefix: string): string {
  const directory = mkdtempSync(join(tmpdir(), prefix));
  temporaryDirectories.push(directory);
  return directory;
}

function temporaryGitRepository(): string {
  const directory = temporaryDirectory("ennote-guard-");
  execFileSync("git", ["init", "--quiet", directory]);
  return directory;
}

function commit(repository: string, message: string, files: Record<string, string>): void {
  for (const [path, content] of Object.entries(files)) {
    const target = join(repository, path);
    mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, content);
  }
  execFileSync("git", ["-C", repository, "add", "--", ...Object.keys(files)]);
  execFileSync("git", [
    "-C", repository,
    "-c", "user.name=guard",
    "-c", "user.email=guard@example.com",
    "commit", "--quiet", "-m", message,
  ]);
}

function commitMessageFile(message: string): string {
  const path = join(temporaryDirectory("ennote-message-"), "COMMIT_EDITMSG");
  writeFileSync(path, message);
  return path;
}

const lockfile = (version: string) => JSON.stringify({ version, packages: { "": { version } } });

afterEach(() => {
  while (temporaryDirectories.length > 0) {
    rmSync(temporaryDirectories.pop() as string, { recursive: true, force: true });
  }
});

describe("repository hygiene guard", () => {
  it("passes on this tree", () => {
    const result = runGuard("check-repo-hygiene.mjs", []);

    expect(result.stderr).toBe("");
    expect(result.status).toBe(0);
  });

  it("fails when a release artifact is tracked", () => {
    const repository = temporaryGitRepository();
    commit(repository, "chore: seed", {
      "package.json": JSON.stringify({ version: "1.0.0" }),
      "package-lock.json": lockfile("1.0.0"),
      "out/index.html": "<html></html>",
    });

    const result = runGuard("check-repo-hygiene.mjs", [], repository);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("out/index.html is tracked");
  });

  it("fails when package-lock.json drifts from package.json", () => {
    const repository = temporaryGitRepository();
    commit(repository, "chore: seed", {
      "package.json": JSON.stringify({ version: "1.0.0" }),
      "package-lock.json": lockfile("0.9.0"),
    });

    const result = runGuard("check-repo-hygiene.mjs", [], repository);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("package-lock.json says 0.9.0 but package.json says 1.0.0");
  });
});

describe("loopback-only guard", () => {
  it("passes on this tree", () => {
    const result = runGuard("check-worker-loopback.mjs", []);

    expect(result.stderr).toBe("");
    expect(result.status).toBe(0);
  });
});

describe("chat-messages pairing guard", () => {
  it("fails when the projection changes without its unit test", () => {
    const repository = temporaryGitRepository();
    commit(repository, "chore: seed", { "lib/chat-messages.ts": "export const a = 1;\n" });
    commit(repository, "refactor(chat): project", { "lib/chat-messages.ts": "export const a = 2;\n" });

    const result = runGuard("check-chat-messages-pairing.mjs", ["--base=HEAD~1"], repository);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("lib/chat-messages.ts changed against HEAD~1 without");
  });

  it("passes when the projection and its unit test change together", () => {
    const repository = temporaryGitRepository();
    commit(repository, "chore: seed", { "lib/chat-messages.ts": "export const a = 1;\n" });
    commit(repository, "refactor(chat): project", {
      "lib/chat-messages.ts": "export const a = 2;\n",
      "tests/unit/chat-messages.test.ts": "it('pins identity', () => {});\n",
    });

    const result = runGuard("check-chat-messages-pairing.mjs", ["--base=HEAD~1"], repository);

    expect(result.stderr).toBe("");
    expect(result.status).toBe(0);
  });

  it("hints instead of failing when no comparison base is available", () => {
    const repository = temporaryGitRepository();
    commit(repository, "chore: seed", { "lib/chat-messages.ts": "export const a = 1;\n" });

    const result = runGuard("check-chat-messages-pairing.mjs", [], repository);

    expect(result.status).toBe(0);
    expect(result.stdout).toContain("pairing guard skipped");
  });
});

describe("Conventional Commit guard", () => {
  it("rejects a code-change type without a scope", () => {
    const result = runGuard("check-commit-message.mjs", ["--message-file", commitMessageFile("feat: add a thing\n")]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain('"feat" requires a scope');
  });

  it("rejects an unknown type", () => {
    const result = runGuard("check-commit-message.mjs", ["--message-file", commitMessageFile("update(worker): nudge\n")]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain('"update" is not one of');
  });

  it("accepts the subjects AGENTS.md spells out", () => {
    for (const header of [
      "feat(chat): encode run status as a code plus detail",
      "test(e2e): cover the offline banner",
      "ci: run Go 1.25 and require the bubblewrap isolation job",
      "chore: bump version to 0.5.0 (agent packs)",
      "fix(gate)!: reject a rebound Host header",
      "Merge branch 'main' into fix/design-review-p0-p1",
    ]) {
      const result = runGuard("check-commit-message.mjs", ["--message-file", commitMessageFile(`${header}\n`)]);

      expect(result.stderr).toBe("");
      expect(result.status).toBe(0);
    }
  });
});

describe("file-size guard", () => {
  it("passes on this tree", () => {
    const result = runGuard("check-file-sizes.mjs", []);
    expect(result.stderr).toBe("");
    expect(result.stdout).toContain("File-size guard passed");
  });

  it("fails when a component outgrows its layer budget", () => {
    const repository = temporaryGitRepository();
    // One line over the 500-line component budget, and no recorded ceiling.
    writeFileSync(join(repository, "package.json"), JSON.stringify({ name: "guard-fixture" }));
    mkdirSync(join(repository, "components"), { recursive: true });
    writeFileSync(join(repository, "components", "Big.tsx"), "x\n".repeat(501));

    const result = runGuard("check-file-sizes.mjs", [], repository);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("components/Big.tsx: 501 lines exceeds the 500-line budget");
  });

  it("fails when a recorded ceiling is exceeded or goes stale", () => {
    const repository = temporaryGitRepository();
    writeFileSync(join(repository, "package.json"), JSON.stringify({ name: "guard-fixture" }));
    mkdirSync(join(repository, "lib"), { recursive: true });
    // A generated file carries no budget: this one would be over if it did.
    writeFileSync(join(repository, "lib", "worker-api.gen.ts"), "x\n".repeat(5000));

    const result = runGuard("check-file-sizes.mjs", [], repository);

    // Nothing to police in this fixture, but the recorded ceilings must not point
    // at files that do not exist: that is how a ratchet silently stops ratcheting.
    expect(result.status).toBe(1);
    expect(result.stderr).toContain("recorded in RECORDED_CEILINGS but not found");
  });
});

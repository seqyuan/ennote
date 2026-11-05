import { createRequire } from "node:module";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";

const root = path.resolve(import.meta.dirname, "../..");
const require = createRequire(import.meta.url);

type EnsureResult = { version: string; fallback: boolean };
type Download = {
  downloadText?: (url: string) => Promise<string>;
  downloadFile?: (url: string, dest: string) => Promise<void>;
};

const launcher = require(path.join(root, "bin/ennote.js")) as {
  cachedBinaryVersions(home: string, suffix: string): string[];
  compareVersions(left: string, right: string): number;
  ensureBinaries(
    home: string,
    pkg: { version: string },
    log: (message: string) => void,
    platform?: string,
    arch?: string,
    download?: Download,
  ): Promise<EnsureResult>;
};

const suffix = "linux-x64";
const homes: string[] = [];

function temporaryHome(): string {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "ennote-binaries-"));
  homes.push(home);
  fs.mkdirSync(path.join(home, "bin"), { recursive: true });
  return home;
}

// A cached build is only usable when both binaries exist, so every helper that
// seeds a version writes the pair unless a test is specifically breaking that.
function seedCachedBinaries(home: string, version: string, names = ["ennogate", "ennoworker"]): void {
  for (const name of names) {
    fs.writeFileSync(path.join(home, "bin", `${name}-${version}-${suffix}`), `${name} ${version}`);
  }
}

function sha256(text: string): string {
  return crypto.createHash("sha256").update(text).digest("hex");
}

// downloader builds a fake release server: `files` are served with matching
// checksums, and `failure` short-circuits the whole transfer.
function downloader(files: Record<string, string>, failure?: Error): Download {
  return {
    async downloadText() {
      if (failure) throw failure;
      return Object.entries(files)
        .map(([name, body]) => `${sha256(body)}  ${name}`)
        .join("\n");
    },
    async downloadFile(url, dest) {
      if (failure) throw failure;
      const name = url.split("/").pop() ?? "";
      const body = files[name];
      if (body === undefined) throw new Error(`HTTP 404 for ${url}`);
      fs.writeFileSync(dest, body);
    },
  };
}

afterEach(() => {
  for (const home of homes.splice(0)) fs.rmSync(home, { recursive: true, force: true });
});

describe("cachedBinaryVersions", () => {
  it("returns only versions whose pair is complete, newest first", () => {
    const home = temporaryHome();
    seedCachedBinaries(home, "0.3.0");
    seedCachedBinaries(home, "0.5.0");
    // Abandoned partial download: gate only.
    seedCachedBinaries(home, "0.4.0", ["ennogate"]);

    expect(launcher.cachedBinaryVersions(home, suffix)).toEqual(["0.5.0", "0.3.0"]);
  });

  it("ignores other platforms and unrelated files", () => {
    const home = temporaryHome();
    seedCachedBinaries(home, "0.5.0");
    fs.writeFileSync(path.join(home, "bin", `ennogate-0.9.0-darwin-arm64`), "mac");
    fs.writeFileSync(path.join(home, "bin", "ennoworker-0.9.0-darwin-arm64"), "mac");
    fs.writeFileSync(path.join(home, "bin", "notes.txt"), "unrelated");

    expect(launcher.cachedBinaryVersions(home, suffix)).toEqual(["0.5.0"]);
  });

  it("treats a missing cache directory as empty", () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), "ennote-binaries-"));
    homes.push(home);
    expect(launcher.cachedBinaryVersions(home, suffix)).toEqual([]);
  });
});

describe("compareVersions", () => {
  it("orders numeric segments, not strings", () => {
    expect(launcher.compareVersions("0.10.0", "0.9.0")).toBeGreaterThan(0);
    expect(launcher.compareVersions("1.0.0", "1.0.0")).toBe(0);
  });

  it("sorts a pre-release before its release", () => {
    expect(launcher.compareVersions("0.5.0-rc1", "0.5.0")).toBeLessThan(0);
    expect(launcher.compareVersions("0.5.0-rc2", "0.5.0-rc1")).toBeGreaterThan(0);
  });
});

describe("ensureBinaries", () => {
  it("does not touch the network when the expected version is cached", async () => {
    const home = temporaryHome();
    seedCachedBinaries(home, "0.5.0");
    let attempts = 0;
    const download: Download = {
      downloadText: async () => { attempts += 1; return ""; },
      downloadFile: async () => { attempts += 1; },
    };

    const result = await launcher.ensureBinaries(home, { version: "0.5.0" }, () => {}, "linux", "x64", download);

    expect(result).toEqual({ version: "0.5.0", fallback: false });
    expect(attempts).toBe(0);
  });

  it("downloads and verifies the expected version when the cache is empty", async () => {
    const home = temporaryHome();
    const files = {
      [`ennogate-${suffix}`]: "gate-bytes",
      [`ennoworker-${suffix}`]: "worker-bytes",
    };

    const result = await launcher.ensureBinaries(home, { version: "0.5.0" }, () => {}, "linux", "x64", downloader(files));

    expect(result).toEqual({ version: "0.5.0", fallback: false });
    expect(fs.readFileSync(path.join(home, "bin", `ennogate-0.5.0-${suffix}`), "utf8")).toBe("gate-bytes");
    expect(fs.readFileSync(path.join(home, "bin", `ennoworker-0.5.0-${suffix}`), "utf8")).toBe("worker-bytes");
  });

  it("falls back to the newest complete cached build and warns when the download fails", async () => {
    const home = temporaryHome();
    seedCachedBinaries(home, "0.4.0");
    seedCachedBinaries(home, "0.3.0");
    const messages: string[] = [];

    const result = await launcher.ensureBinaries(
      home,
      { version: "0.5.0" },
      (message) => messages.push(message),
      "linux",
      "x64",
      downloader({}, new Error("getaddrinfo ENOTFOUND github.com")),
    );

    expect(result).toEqual({ version: "0.4.0", fallback: true });
    expect(messages.join("\n")).toContain("warning:");
    expect(messages.join("\n")).toContain("0.4.0");
    expect(messages.join("\n")).toContain("0.5.0");
  });

  it("fails with an actionable error when there is no cached build to fall back to", async () => {
    const home = temporaryHome();

    await expect(
      launcher.ensureBinaries(home, { version: "0.5.0" }, () => {}, "linux", "x64",
        downloader({}, new Error("getaddrinfo ENOTFOUND github.com"))),
    ).rejects.toThrow(/no complete cached build/u);

    await expect(
      launcher.ensureBinaries(home, { version: "0.5.0" }, () => {}, "linux", "x64",
        downloader({}, new Error("getaddrinfo ENOTFOUND github.com"))),
    ).rejects.toThrow(new RegExp(path.join(home, "bin"), "u"));
  });

  it("removes a downloaded binary whose checksum does not match", async () => {
    const home = temporaryHome();
    // The sums file advertises the real hash; the transfer is corrupted.
    const download: Download = {
      downloadText: async () => `${"0".repeat(64)}  ennogate-${suffix}`,
      downloadFile: async (_url, dest) => { fs.writeFileSync(dest, "corrupted"); },
    };

    await expect(
      launcher.ensureBinaries(home, { version: "0.5.0" }, () => {}, "linux", "x64", download),
    ).rejects.toThrow(/checksum mismatch/u);

    expect(fs.existsSync(path.join(home, "bin", `ennogate-0.5.0-${suffix}`))).toBe(false);
  });

  it("prefers an older complete build over the partially downloaded current one", async () => {
    const home = temporaryHome();
    seedCachedBinaries(home, "0.4.0");
    // Only the gate of the current version made it to disk.
    seedCachedBinaries(home, "0.5.0", ["ennogate"]);

    const result = await launcher.ensureBinaries(
      home,
      { version: "0.5.0" },
      () => {},
      "linux",
      "x64",
      downloader({}, new Error("ETIMEDOUT")),
    );

    expect(result).toEqual({ version: "0.4.0", fallback: true });
  });
});

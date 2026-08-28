import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const root = resolve(import.meta.dirname, "../..");
const require = createRequire(import.meta.url);
const launcher = require(resolve(root, "bin/ennote.js")) as {
  platformSuffix(platform: string, arch: string): string;
  runtimeBinDir(home: string): string;
  runtimePaths(packageDir: string, home: string, version: string, platform: string, arch: string): {
    gate: string;
    worker: string;
    staticDir: string;
  };
  statePaths(home: string): { state: string; log: string; auth: string };
  serviceURL(hostname: string, port: string): string;
  validateRuntime(paths: { gate: string; worker: string; staticDir: string }): void;
  parseSHA256SUMS(text: string, filename: string): string;
  fileSHA256(filePath: string): string;
  verifyFileSHA256(filePath: string, expected: string): void;
};

describe("ennote launcher", () => {
  it("selects matching ennogate and ennoworker binaries", () => {
    expect(launcher.platformSuffix("linux", "x64")).toBe("linux-x64");
    expect(launcher.platformSuffix("darwin", "arm64")).toBe("darwin-arm64");
    expect(() => launcher.platformSuffix("win32", "x64")).toThrow("Unsupported platform");

    const paths = launcher.runtimePaths("/package", "/home", "1.0.0", "linux", "arm64");
    // Falls back to bundled worker/ when cache is empty
    expect(paths.gate).toBe("/package/worker/ennogate-linux-arm64");
    expect(paths.worker).toBe("/package/worker/ennoworker-linux-arm64");
    expect(paths.staticDir).toBe("/package/out");
  });

  it("formats IPv4, wildcard and IPv6 service URLs", () => {
    expect(launcher.serviceURL("127.0.0.1", "45131")).toBe("http://127.0.0.1:45131");
    expect(launcher.serviceURL("0.0.0.0", "45131")).toBe("http://127.0.0.1:45131");
    expect(launcher.serviceURL("::1", "45131")).toBe("http://[::1]:45131");
  });

  it("uses the same authentication file as ennogate", () => {
    expect(launcher.statePaths("/home/ennote")).toEqual({
      state: "/home/ennote/ennote-state.json",
      log: "/home/ennote/logs/ennote.log",
      auth: "/home/ennote/config/auth.json",
    });
  });

  it("parses SHA256SUMS and rejects a mismatched digest", () => {
    const sums = [
      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  ennogate-linux-x64",
      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  ennoworker-darwin-arm64",
    ].join("\n");
    expect(launcher.parseSHA256SUMS(sums, "ennogate-linux-x64")).toBe("a".repeat(64));
    expect(launcher.parseSHA256SUMS(sums, "ennoworker-darwin-arm64")).toBe("b".repeat(64));
    expect(() => launcher.parseSHA256SUMS(sums, "missing")).toThrow("no entry");
    const tmp = resolve(root, "tests/unit/checksum-fixture.bin");
    require("node:fs").writeFileSync(tmp, "payload");
    expect(() => launcher.verifyFileSHA256(tmp, "a".repeat(64))).toThrow("checksum mismatch");
    launcher.verifyFileSHA256(tmp, launcher.fileSHA256(tmp));
    require("node:fs").unlinkSync(tmp);
  });

  it("fails clearly when packaged runtime files are absent", () => {
    expect(() => launcher.validateRuntime({
      gate: "/missing/ennogate",
      worker: "/missing/ennoworker",
      staticDir: "/missing/out",
    })).toThrow("Missing ennogate binary");
  });

  it("reports help and package version without starting services", () => {
    const help = execFileSync(process.execPath, [resolve(root, "bin/ennote.js"), "--help"], { encoding: "utf8" });
    expect(help).toContain("Start the ennogate service");
    expect(help).not.toContain("Next.js");
    const pkg = require(resolve(root, "package.json")) as { version: string };
    const version = execFileSync(process.execPath, [resolve(root, "bin/ennote.js"), "--version"], { encoding: "utf8" });
    expect(version.trim()).toBe(pkg.version);
  });
});

import { describe, expect, it } from "vitest";

import { apiKeyFailure } from "../../lib/api-key";

describe("apiKeyFailure", () => {
  it("returns undefined for empty (keep stored key / env auth)", () => {
    expect(apiKeyFailure("")).toBeUndefined();
  });

  it("flags whitespace-only input", () => {
    expect(apiKeyFailure("   ")).toBe("keyBlank");
  });

  it("rejects non-printable-ASCII characters", () => {
    expect(apiKeyFailure("sk-\u00e9")).toBe("keyIllegalCharacters");
    // Leading/trailing whitespace is trimmed first (a trailing newline from a
    // paste is not a failure), so an interior control character is the case
    // that must be refused.
    expect(apiKeyFailure("sk-\nabc")).toBe("keyIllegalCharacters");
  });

  it("rejects pasted NAME=value lines and quoted values", () => {
    expect(apiKeyFailure("MY_KEY=abc")).toBe("keyIllegalCharacters");
    expect(apiKeyFailure("\"abc\"")).toBe("keyIllegalCharacters");
    expect(apiKeyFailure("'abc'")).toBe("keyIllegalCharacters");
  });

  it("accepts a normal key", () => {
    expect(apiKeyFailure("sk-abc123-._")).toBeUndefined();
  });
});

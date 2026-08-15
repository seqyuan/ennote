import { describe, expect, it } from "vitest";

import { errorMessage, runFailureMessage } from "../../lib/provider-errors";

describe("Provider run errors", () => {
  it("maps stable Provider codes to safe user-facing messages", () => {
    expect(runFailureMessage("provider_authentication_failed")).toBe("The provider rejected the configured credential.");
    expect(runFailureMessage("provider_credential_unavailable")).toBe("The configured credential could not be resolved.");
    expect(runFailureMessage("provider_rate_limited")).not.toContain("429");
  });

  it("does not reuse a raw backend error message for unknown failures", () => {
    expect(runFailureMessage("new_failure")).toBe("The run failed (new_failure).");
    expect(runFailureMessage(undefined)).toBe("The run failed.");
  });
});

describe("errorMessage", () => {
  it("returns a real Error message", () => {
    expect(errorMessage(new Error("boom"), "fallback")).toBe("boom");
  });

  it("normalizes a bare string rejection", () => {
    expect(errorMessage("network down", "fallback")).toBe("network down");
  });

  it("falls back for a non-Error, non-string rejection instead of an empty state", () => {
    expect(errorMessage(undefined, "fallback")).toBe("fallback");
    expect(errorMessage(null, "fallback")).toBe("fallback");
    expect(errorMessage({ code: 500 }, "fallback")).toBe("fallback");
  });

  it("ignores an empty Error message and uses the fallback", () => {
    expect(errorMessage(new Error(""), "fallback")).toBe("fallback");
  });
});

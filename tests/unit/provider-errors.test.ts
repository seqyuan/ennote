import { describe, expect, it } from "vitest";

import { runFailureMessage } from "../../lib/provider-errors";

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

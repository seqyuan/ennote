import { describe, expect, it } from "vitest";
import { WorkerRequestError, isSourceDigestConflict } from "@/lib/worker-api.client";

// Every non-2xx used to surface as a bare Error with only a message, so a caller
// could not tell a stale-file digest conflict from an ordinary 422 validation
// failure. The editors reported both as "File changed on disk" and disabled
// Publish. status and code are now carried on the error.
describe("WorkerRequestError", () => {
  it("keeps the message, status and code", () => {
    const error = new WorkerRequestError("hero.yaml changed", 409, "source_digest_conflict");

    expect(error.message).toBe("hero.yaml changed");
    expect(error.status).toBe(409);
    expect(error.code).toBe("source_digest_conflict");
    expect(error).toBeInstanceOf(Error);
  });
});

describe("isSourceDigestConflict", () => {
  it("treats a 409 as a digest conflict", () => {
    expect(isSourceDigestConflict(new WorkerRequestError("changed", 409))).toBe(true);
  });

  it("treats the conflict code as a digest conflict even without 409", () => {
    expect(isSourceDigestConflict(new WorkerRequestError("changed", 400, "source_digest_conflict"))).toBe(true);
  });

  it("does not treat a 422 validation failure as a conflict", () => {
    expect(isSourceDigestConflict(
      new WorkerRequestError("permissionCeiling must be one of discuss, ask, auto", 422, "hero_validation_failed"),
    )).toBe(false);
  });

  it("does not treat a plain Error as a conflict", () => {
    expect(isSourceDigestConflict(new Error("something else"))).toBe(false);
  });
});

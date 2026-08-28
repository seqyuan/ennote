import { describe, expect, it } from "vitest";
import { recentProjectId } from "@/lib/ensure-blank-session";
import { isBlankSession } from "@/lib/session-blank";

describe("recentProjectId", () => {
  it("returns undefined for an empty catalog", () => {
    expect(recentProjectId([])).toBeUndefined();
  });

  it("picks the project with the latest updatedAt", () => {
    expect(recentProjectId([
      { id: "old", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" },
      { id: "new", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-08-01T00:00:00Z" },
    ])).toBe("new");
  });
});

describe("isBlankSession", () => {
  it("treats a missing leaf as blank", () => {
    expect(isBlankSession({})).toBe(true);
    expect(isBlankSession({ activeLeafMessageId: "m1" })).toBe(false);
    expect(isBlankSession(null)).toBe(false);
  });
});

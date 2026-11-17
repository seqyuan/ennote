import { describe, expect, it } from "vitest";

import { shortDigest, summariseGraphIdentity } from "@/lib/graph-identity";

const draft = "sha256:" + "a".repeat(64);
const published = { version: 7, digest: "sha256:" + "b".repeat(64), publishedAt: "2026-11-14T00:00:00Z" };

describe("summariseGraphIdentity", () => {
  it("says the draft is unpublished when no revision matches it", () => {
    const identity = summariseGraphIdentity({ draftDigest: draft, published, bindings: [] });
    expect(identity.draftIsPublished).toBe(false);
    expect(identity.published).toEqual({ version: 7, digest: published.digest });
  });

  it("says the draft is published when the digests agree", () => {
    const identity = summariseGraphIdentity({ draftDigest: published.digest, published, bindings: [] });
    expect(identity.draftIsPublished).toBe(true);
  });

  it("reports an unpublished Graph as never published", () => {
    const identity = summariseGraphIdentity({ draftDigest: draft, published: null, bindings: [] });
    expect(identity.published).toBeNull();
    expect(identity.draftIsPublished).toBe(false);
  });

  it("does not call an empty draft digest equal to an empty publication", () => {
    // Both sides missing must not read as "in sync".
    const identity = summariseGraphIdentity({ draftDigest: null, published: null, bindings: [] });
    expect(identity.draftIsPublished).toBe(false);
  });

  it("counts only enabled, pinned bindings as runnable", () => {
    const identity = summariseGraphIdentity({
      draftDigest: draft,
      published,
      bindings: [
        { projectId: "proj-b", revision: "v000007", enabled: true },
        { projectId: "proj-a", revision: "v000006", enabled: true },
        { projectId: "proj-c", revision: "v000007", enabled: false },
        { projectId: "proj-d", revision: "", enabled: true },
      ],
    });
    expect(identity.bound).toBe(true);
    expect(identity.runnable).toEqual([
      { projectId: "proj-a", revision: "v000006" },
      { projectId: "proj-b", revision: "v000007" },
    ]);
    // An enabled binding with no revision pins nothing; it is reported apart and
    // must never be rendered as a version.
    expect(identity.unpinned).toEqual(["proj-d"]);
  });

  it("distinguishes never bound from bound but off", () => {
    expect(summariseGraphIdentity({ draftDigest: draft, published, bindings: [] }).bound).toBe(false);
    const off = summariseGraphIdentity({
      draftDigest: draft, published,
      bindings: [{ projectId: "proj-a", revision: "v000007", enabled: false }],
    });
    expect(off.bound).toBe(true);
    expect(off.runnable).toEqual([]);
  });
});

describe("shortDigest", () => {
  it("keeps the first eight hex characters and drops the algorithm", () => {
    expect(shortDigest(draft)).toBe("aaaaaaaa");
    expect(shortDigest("abc")).toBe("abc");
  });
});

describe("a degraded Worker answer", () => {
  it("renders a missing digest as a dash instead of throwing", () => {
    // The header is decoration; a response without a digest must not reach the
    // error boundary and blank the editor.
    expect(shortDigest(undefined)).toBe("—");
    expect(shortDigest(null)).toBe("—");
    expect(shortDigest("")).toBe("—");
  });

  it("does not call a publication without a digest 'in sync' with the draft", () => {
    const identity = summariseGraphIdentity({
      draftDigest: "",
      published: { version: 3, digest: undefined as unknown as string, publishedAt: "2026-01-01T00:00:00Z" },
      bindings: [],
    });
    expect(identity.draftIsPublished).toBe(false);
    expect(identity.published).toEqual({ version: 3, digest: "" });
  });
});

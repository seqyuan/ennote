import type { components } from "@/lib/worker-api.gen";

type GraphBindingRef = components["schemas"]["GraphBindingRef"];

/** The published revision a Graph's header reports, or null when it has none. */
export type PublishedRevision = { version: number; digest: string; publishedAt: string } | null;

export type GraphIdentity = {
  draftDigest: string;
  /** True when the draft is exactly the latest published revision. */
  draftIsPublished: boolean;
  published: { version: number; digest: string } | null;
  /** Bindings that would actually run something: enabled, with a pinned revision. */
  runnable: { projectId: string; revision: string }[];
  /** Enabled bindings that pin no revision: reported separately, never as a version. */
  unpinned: string[];
  bound: boolean;
};

/**
 * summariseGraphIdentity answers the four questions a Graph header exists to
 * answer: what is the draft, what is the latest published revision, which Projects
 * would run it at which revision, and (in the UI) whether the last Run was the same
 * assembly.
 *
 * The distinction that matters is that a draft is never what runs. A Run needs a
 * published revision *and* a Project binding that is enabled and pinned, so the
 * editor must not let "Draft saved" read as "this will run". An enabled binding
 * with an empty revision pins nothing and is reported as such rather than as a
 * version the reader might take for the one in use.
 */
export function summariseGraphIdentity(input: {
  draftDigest: string | null;
  published: PublishedRevision;
  bindings: readonly GraphBindingRef[];
}): GraphIdentity {
  const runnable: GraphIdentity["runnable"] = [];
  const unpinned: string[] = [];
  let bound = false;
  for (const binding of input.bindings) {
    bound = true;
    if (!binding.enabled) continue;
    if (binding.revision === "") {
      unpinned.push(binding.projectId);
      continue;
    }
    runnable.push({ projectId: binding.projectId, revision: binding.revision });
  }
  runnable.sort((left, right) => left.projectId.localeCompare(right.projectId));
  unpinned.sort();
  return {
    draftDigest: input.draftDigest ?? "",
    // An absent digest is not "equal to the draft": a Worker answer that omits the
    // field must not read as "in sync", and it must not throw on the way to the
    // header either.
    draftIsPublished: (input.published?.digest ?? "") !== ""
      && input.published?.digest === input.draftDigest,
    published: input.published === null
      ? null
      : { version: input.published.version, digest: input.published.digest ?? "" },
    runnable,
    unpinned,
    bound,
  };
}

/**
 * shortDigest keeps a digest recognisable in a header without the noise.
 *
 * Total on purpose: a response that omits a digest renders as "—" rather than
 * throwing inside the header, where the error boundary would blank the whole
 * editor over a missing decoration.
 */
export function shortDigest(digest: string | null | undefined): string {
  if (!digest) return "—";
  const bare = digest.startsWith("sha256:") ? digest.slice("sha256:".length) : digest;
  return bare.slice(0, 8);
}

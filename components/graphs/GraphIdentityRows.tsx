"use client";

import { shortDigest, type GraphIdentity } from "@/lib/graph-identity";

/**
 * The four facts a Graph header has to keep apart.
 *
 * A draft is never what runs: a Run needs a published revision and a Project
 * binding that is enabled and pinned to it. The previous header printed
 * "Saved v7" — a save state wearing a version number — so a reader could not tell
 * whether the edits on screen were in v7, or what any Project would actually run.
 *
 * "Last run" stays a placeholder until the Worker records an assembly digest; it
 * says so rather than showing the binding and letting the reader assume the Run
 * was the same assembly.
 */
export function GraphIdentityRows({ identity, runsChannelName }: {
  identity: GraphIdentity;
  runsChannelName?: string;
}) {
  return (
    <dl className="graph-identity">
      <div>
        <dt>Draft</dt>
        <dd>
          <code>{identity.draftDigest ? shortDigest(identity.draftDigest) : "—"}</code>
          <small>{identity.draftIsPublished ? "In published revision" : "Not published"}</small>
        </dd>
      </div>
      <div>
        <dt>Published</dt>
        <dd>
          {identity.published === null
            ? <small>Never published</small>
            : <>
                {/* The version is its own element: "v7" is a fact, not a prefix of
                    the digest, and the tests and screen readers read it alone. */}
                <strong>v{identity.published.version}</strong>
                <code>{shortDigest(identity.published.digest)}</code>
              </>}
        </dd>
      </div>
      <div>
        <dt>Runs from</dt>
        <dd>
          {!identity.bound && <small>Not bound to any Project</small>}
          {identity.runnable.map((binding) => (
            <span key={binding.projectId}>
              <code>{binding.projectId}</code>
              <strong>{binding.revision}</strong>
            </span>
          ))}
          {identity.unpinned.map((projectId) => (
            <span key={projectId}>
              <code>{projectId}</code>
              <em>not pinned</em>
            </span>
          ))}
        </dd>
      </div>
      <div>
        <dt>Last run</dt>
        <dd><small>{runsChannelName ? `Assembly not recorded — see ${runsChannelName}` : "Assembly not recorded"}</small></dd>
      </div>
    </dl>
  );
}

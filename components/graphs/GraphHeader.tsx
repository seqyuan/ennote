"use client";

import { Check, Loader2 } from "lucide-react";
import { GraphIdentityRows } from "@/components/graphs/GraphIdentityRows";
import type { FlowDefinition } from "@/components/graphs/types";
import type { GraphIdentity } from "@/lib/graph-identity";

/**
 * The Graph editor's header: which Graph this is, the four identity facts, and the
 * save/publish controls.
 *
 * Presentational on purpose — the save state, the identity and the publish handler
 * all come from the editor's save session, and keeping the markup here is what
 * lets that session read top-to-bottom in one file.
 */
export function GraphHeader({ definition, identity, saveState, publishing, publishDisabled, onPublish }: {
  definition: FlowDefinition;
  identity: GraphIdentity;
  saveState: "saving" | "unsaved" | "saved";
  publishing: boolean;
  publishDisabled: boolean;
  onPublish: () => void;
}) {
  return (
    <header className="graph-document-header">
      <div><span>Graph</span><h2>{definition.name ?? definition.id}</h2><code>{definition.id}</code></div>
      <GraphIdentityRows identity={identity} />
      <div className="graph-document-actions">
        <div className={`graph-save-state is-${saveState}`} role="status">{saveState === "saving"
          ? <><Loader2 className="spin" size={14} /> Saving</>
          : saveState === "unsaved"
            ? <>Unsaved changes</>
            : <><Check size={14} /> Draft saved</>}</div>
        <button type="button" onClick={onPublish} disabled={publishDisabled}>{publishing ? "Publishing…" : "Publish"}</button>
      </div>
    </header>
  );
}

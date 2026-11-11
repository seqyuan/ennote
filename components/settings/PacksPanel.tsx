"use client";

import { useEffect, useState } from "react";
import { Package, RefreshCw, Trash2 } from "lucide-react";

import {
  installPack,
  inspectPack,
  listPacks,
  packIssues,
  removePack,
  type PackInstallPlan,
  type PackInspectResult,
  type PackStateSummary,
} from "@/lib/packs";
import styles from "./PacksPanel.module.css";

/**
 * Agent Packs: what is installed, and what a pack would do before it is
 * installed.
 *
 * The panel never decides whether a pack is acceptable — it shows the Worker's
 * own answer. A real install is only offered after a dry run comes back without
 * blockers, because the Worker refuses an install that still has any.
 */
export function PacksPanel({ setError }: { setError: (value: string | null) => void }) {
  const [installed, setInstalled] = useState<PackStateSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [inspected, setInspected] = useState<PackInspectResult | null>(null);
  const [plan, setPlan] = useState<PackInstallPlan | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  const load = async () => {
    try {
      setInstalled(await listPacks());
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setLoading(false);
    }
  };

  // One read per open, deferred so the panel paints before the request lands.
  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    try {
      await work();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const inspect = () => run(async () => {
    setPlan(null);
    setInspected(await inspectPack(path.trim()));
  });

  const dryRun = () => run(async () => {
    setPlan(await installPack(path.trim(), { dryRun: true }));
  });

  const apply = () => run(async () => {
    const applied = await installPack(path.trim(), {
      dryRun: false,
      ...(inspected ? { expectedDigest: inspected.packDigest } : {}),
    });
    setPlan(null);
    setInspected(null);
    setPath("");
    await load();
    setError(applied.blockers.length > 0 ? applied.blockers[0].message : null);
  });

  const uninstall = (pack: PackStateSummary, force: boolean) => run(async () => {
    setRemoving(pack.pack.id);
    try {
      await removePack(publisherOf(pack.pack.id), pack.pack.name, force);
      await load();
    } finally {
      setRemoving(null);
    }
  });

  const blocked = plan !== null && plan.blockers.length > 0;

  return (
    <section className="settings-tab-section" aria-labelledby="settings-packs-heading">
      <h3 id="settings-packs-heading" className={styles.heading}>Agent Packs</h3>
      <p className={styles.note}>
        A pack ships Heroes, Skills, and optionally a Graph or Agent Flow. Installing one only creates
        disabled drafts on this Worker — nothing runs until a revision is published and bound.
      </p>

      <div className={styles.installer}>
        <label className={styles.pathLabel}>
          <span>Pack path on the Worker host</span>
          <input
            className={styles.pathInput}
            value={path}
            placeholder="/home/me/packs/example.enpack"
            onChange={(event) => setPath(event.target.value)}
          />
        </label>
        <div className={styles.actions}>
          <button type="button" className={styles.button} disabled={busy || path.trim() === ""} onClick={inspect}>
            Inspect
          </button>
          <button type="button" className={styles.button} disabled={busy || path.trim() === ""} onClick={dryRun}>
            Check install
          </button>
          <button
            type="button"
            className={`${styles.button} ${styles.primary}`}
            disabled={busy || path.trim() === "" || plan === null || blocked}
            onClick={apply}
          >
            Install
          </button>
        </div>
      </div>

      {inspected && (
        <div className={styles.card} data-testid="pack-inspect">
          <div className={styles.cardHead}>
            <Package size={14} aria-hidden="true" />
            <span className={styles.cardName}>{inspected.pack.name}</span>
            <span className={styles.badge}>{inspected.pack.version}</span>
            <span className={styles.badge}>{inspected.pack.id}</span>
            {!inspected.signed && <span className={styles.badge}>unsigned</span>}
          </div>
          <div className={styles.counts}>
            {inspected.flows.length} flows · {inspected.heroes.length} heroes · {inspected.skills.length} skills
            · {inspected.filesTotal} files
          </div>
          <div className={styles.digest}>pack digest {inspected.packDigest.slice(0, 16)}…</div>
          <IssueList issues={inspected.warnings} />
        </div>
      )}

      {plan && (
        <div className={styles.card} data-testid="pack-plan" data-blocked={blocked || undefined}>
          <div className={styles.cardHead}>
            <span className={styles.cardName}>Install plan</span>
            <span className={styles.badge}>{plan.actions.length} actions</span>
            {plan.willPublishFlowRevision && <span className={styles.badge}>publishes a revision</span>}
          </div>
          <ul className={styles.actionList}>
            {plan.actions.map((action) => (
              <li key={`${action.kind}:${action.storeId}`} className={styles.actionRow}>
                <span className={styles.badge}>{action.kind}</span>
                <span className={styles.actionId}>{action.storeId}</span>
                <span className={styles.actionVerb} data-action={action.action}>{action.action}</span>
              </li>
            ))}
          </ul>
          <IssueList issues={packIssues(plan)} />
        </div>
      )}

      <div className={styles.sectionHeader}>
        Installed <span className={styles.sectionCount}>{installed.length}</span>
        <button type="button" className={styles.iconButton} aria-label="Refresh installed packs" onClick={() => void load()}>
          <RefreshCw size={13} aria-hidden="true" />
        </button>
      </div>

      {loading ? (
        <div className={styles.empty}>Loading…</div>
      ) : installed.length === 0 ? (
        <div className={styles.empty}>No packs installed.</div>
      ) : (
        <div className={styles.stack}>
          {installed.map((pack) => (
            <div key={pack.pack.id} className={styles.row} data-testid="pack-row">
              <div className={styles.identity}>
                <div className={styles.titleRow}>
                  <span className={styles.name}>{pack.pack.name}</span>
                  <span className={styles.badge}>{pack.pack.version}</span>
                </div>
                <div className={styles.meta}>
                  {pack.flowCount} flows · {pack.heroCount} heroes · {pack.skillCount} skills
                </div>
              </div>
              <button
                type="button"
                className={styles.iconButton}
                aria-label={`Remove ${pack.pack.name}`}
                disabled={busy || removing === pack.pack.id}
                onClick={() => uninstall(pack, false)}
              >
                <Trash2 size={13} aria-hidden="true" />
              </button>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function IssueList({ issues }: { issues: { code: string; severity?: string; message: string }[] }) {
  if (issues.length === 0) return null;
  return (
    <ul className={styles.issueList}>
      {issues.map((issue) => (
        <li key={`${issue.code}:${issue.message}`} className={styles.issueRow} data-severity={issue.severity ?? "error"}>
          <span className={styles.badge}>{issue.code}</span>
          <span>{issue.message}</span>
        </li>
      ))}
    </ul>
  );
}

/** A pack id is `<publisher>/<name>`; the endpoint wants the publisher alone. */
function publisherOf(packId: string): string {
  const index = packId.indexOf("/");
  return index < 0 ? packId : packId.slice(0, index);
}

"use client";

import { useEffect, useState } from "react";
import { Package, RefreshCw, Trash2 } from "lucide-react";

import {
  installPack,
  inspectPack,
  listPacks,
  packIssues,
  packSlug,
  removePack,
  type PackInstallPlan,
  type PackInspectResult,
  type PackStateSummary,
} from "@/lib/packs";
import { confirmAction } from "@/components/ui/confirm";
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
  const [approvedPath, setApprovedPath] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [forceOffer, setForceOffer] = useState<string | null>(null);

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
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await work();
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  };

  // Any edit to the path voids what was approved for the previous one: an
  // approved plan must never be applied to a different pack.
  const changePath = (next: string) => {
    setPath(next);
    setPlan(null);
    setApprovedPath(null);
    setInspected(null);
    setForceOffer(null);
  };

  const inspect = () => run(async () => {
    setPlan(null);
    setApprovedPath(null);
    setInspected(await inspectPack(path.trim()));
  });

  const dryRun = () => run(async () => {
    setPlan(await installPack(path.trim(), { dryRun: true }));
    setApprovedPath(path.trim());
  });

  const apply = async () => {
    if (!plan || approvedPath !== path.trim()) return;
    const approved = await confirmAction({
      title: `Install ${plan.pack.name} ${plan.pack.version}?`,
      description: "This creates disabled draft objects on this Worker. Nothing runs until a revision is published and bound.",
      confirmLabel: "Install",
    });
    if (!approved) return;
    await run(async () => {
      // Approve the exact bytes that were planned, not whatever sits there now.
      await installPack(path.trim(), { dryRun: false, expectedDigest: plan.packDigest });
      setPlan(null);
      setApprovedPath(null);
      setInspected(null);
      changePath("");
      await load();
    });
  };

  const uninstall = async (pack: PackStateSummary, force: boolean) => {
    const slug = packSlug(pack.pack.id);
    if (!slug) {
      setError(`"${pack.pack.id}" is not a publisher/name pack id.`);
      return;
    }
    const approved = await confirmAction({
      title: force ? `Force-remove ${pack.pack.name}?` : `Remove ${pack.pack.name}?`,
      description: force
        ? "This also deletes objects that were edited locally or still referenced by another Graph."
        : "Every object this pack installed is deleted. A locally edited Skill is kept, with a warning.",
      confirmLabel: force ? "Force remove" : "Remove",
      tone: "danger",
    });
    if (!approved) return;
    await run(async () => {
      setRemoving(pack.pack.id);
      try {
        await removePack(slug.publisher, slug.name, force);
        setForceOffer(null);
        await load();
      } catch (reason) {
        // A refusal here is usually "still referenced" or "edited locally": both
        // are what force exists for, so offer it instead of only printing.
        setForceOffer(pack.pack.id);
        throw reason;
      } finally {
        setRemoving(null);
      }
    });
  };

  const planApplies = plan !== null && approvedPath === path.trim();
  const blocked = !planApplies || plan.blockers.length > 0;

  return (
    <section className="settings-tab-section" aria-labelledby="settings-packs-heading">
      <h3 id="settings-packs-heading" className={styles.heading}>Agent Packs</h3>
      <p className={styles.note}>
        A pack ships Heroes, Skills, and optionally a Graph. Installing one only creates
        disabled drafts on this Worker — nothing runs until a revision is published and bound.
      </p>

      <div className={styles.installer}>
        <label className={styles.pathLabel}>
          <span>Pack path on the Worker host</span>
          <input
            className={styles.pathInput}
            value={path}
            placeholder="/home/me/packs/example.enpack"
            onChange={(event) => changePath(event.target.value)}
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
            disabled={busy || path.trim() === "" || blocked}
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
              {forceOffer === pack.pack.id && (
                <button
                  type="button"
                  className={styles.button}
                  disabled={busy || removing === pack.pack.id}
                  onClick={() => void uninstall(pack, true)}
                >
                  Force remove
                </button>
              )}
              <button
                type="button"
                className={styles.iconButton}
                aria-label={`Remove ${pack.pack.name}`}
                disabled={busy || removing === pack.pack.id}
                onClick={() => void uninstall(pack, false)}
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
        <li key={`${issue.code}:${issue.message}`} className={styles.issueRow} data-severity={issue.severity ?? "warning"}>
          <span className={styles.badge}>{issue.code}</span>
          <span>{issue.message}</span>
        </li>
      ))}
    </ul>
  );
}

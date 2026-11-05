"use client";

import styles from "./OfflineBanner.module.css";

/**
 * Read-only notice for Offline App state (AGENTS.md). Nothing is disabled here:
 * apiResponse refuses a mutation once the Worker is unreachable, and a rejected
 * submit hands the draft back to the composer, so the banner only has to say
 * why. The signal is the Worker's reachability, not the browser's online flag —
 * the loopback Worker keeps serving with no network at all.
 */
export function OfflineBanner({ workerUnreachable }: { workerUnreachable: boolean }) {
  if (!workerUnreachable) return null;
  return (
    <div className={styles.banner} role="status" data-testid="offline-banner">
      Worker unreachable — Ennote is read-only until it returns.
    </div>
  );
}

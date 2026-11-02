"use client";

import styles from "./OfflineBanner.module.css";

/**
 * Read-only notice for Offline App state (AGENTS.md). Nothing is disabled here:
 * apiResponse already refuses a mutation while offline, and a rejected submit
 * hands the draft back to the composer, so the banner only has to say why.
 */
export function OfflineBanner({ offline }: { offline: boolean }) {
  if (!offline) return null;
  return (
    <div className={styles.banner} role="status" data-testid="offline-banner">
      Offline: Ennote is read-only until the connection returns.
    </div>
  );
}

"use client";

/**
 * The two banners between the Graph header and the editor body.
 *
 * They are presentational on purpose: every decision (retry, keep mine, discard)
 * belongs to the editor's save session, and keeping the markup here is what lets
 * GraphSettings stay under its layer budget while the queue logic reads
 * top-to-bottom.
 */
export type SaveErrorBanner = { message: string };
export type ConflictBanner = { reason: "conflict" | "reload" };
export type DiskPreview = { name: string; changed: string[] };

export function GraphSaveBanners({ saveError, conflict, saving, diskPreview, onRetry, onViewDisk, onKeepMine, onDiscardMine }: {
  saveError: SaveErrorBanner | null;
  conflict: ConflictBanner | null;
  saving: boolean;
  diskPreview: DiskPreview | null;
  onRetry: () => void;
  onViewDisk: () => void;
  onKeepMine: () => void;
  onDiscardMine: () => void;
}) {
  return (
    <>
      {saveError && (
        <div className="graph-save-error" role="alert">
          <span>{saveError.message}</span>
          <button type="button" onClick={onRetry}>Retry save</button>
        </div>
      )}

      {conflict && (
        <div className="graph-conflict-bar" role="status">
          <span>{conflict.reason === "reload"
            ? "This Graph changed while you had unsaved edits."
            : "The file changed on disk."}</span>
          {/* A conflict is decided once the save in flight has landed: until then
              the disk read is stale and the in-flight answer would overwrite the
              base the decision just chose. */}
          <button type="button" disabled={saving} onClick={onViewDisk}>View disk version</button>
          <button type="button" disabled={saving} onClick={onKeepMine}>Keep mine</button>
          <button type="button" disabled={saving} onClick={onDiscardMine}>Discard mine</button>
          {saving && <small>A save is still in flight — decide when it finishes.</small>}
          {diskPreview && (
            <div className="graph-conflict-preview">
              <span>On disk: {diskPreview.name}</span>
              <span>{diskPreview.changed.length === 0
                ? "No Task differences."
                : `Differing Tasks: ${diskPreview.changed.join(", ")}`}</span>
            </div>
          )}
        </div>
      )}
    </>
  );
}

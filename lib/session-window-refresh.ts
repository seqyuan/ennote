/**
 * The deferred window read behind a session change feed.
 *
 * A `message_committed` frame says "canonical rows changed", not "read them
 * now". The Authority emits it in the same transaction as the run's terminal
 * frame, so the terminal frame normally arrives in the same batch and takes the
 * read; refreshing on both would issue a pair of requests only to abort it a
 * moment later. This class holds that intent instead of the requests.
 *
 * The intent outlives a pause on purpose. A paused store drops its timer but
 * keeps `dirty`, because the frames that dirtied the window are already behind
 * the stream cursor and will never replay: forgetting the intent there would
 * lose committed rows from the timeline for good.
 */
export const COMMIT_REFRESH_DEBOUNCE_MS = 400;

export class WindowRefresh {
  private timer: ReturnType<typeof setTimeout> | null = null;
  private dirty = false;

  constructor(private readonly read: () => Promise<void>) {}

  /** Marks the window stale and arms the fallback read. */
  markDirty(): void {
    this.dirty = true;
    if (this.timer) return;
    this.timer = setTimeout(() => {
      this.timer = null;
      this.dirty = false;
      void this.read();
    }, COMMIT_REFRESH_DEBOUNCE_MS);
  }

  /** Reads now, superseding a pending fallback: a boundary frame is one. */
  flush(): void {
    this.cancelPending();
    this.dirty = false;
    void this.read();
  }

  /** Drops the timer, keeps the intent. */
  cancelPending(): void {
    if (!this.timer) return;
    clearTimeout(this.timer);
    this.timer = null;
  }

  /**
   * Reports whether a read was interrupted and consumes the intent, so the caller
   * performs it exactly once.
   */
  takeInterruptedRead(): boolean {
    if (!this.dirty) return false;
    this.dirty = false;
    return true;
  }

  /** The window is being replaced wholesale: there is nothing left to read. */
  replaceWindow(): void {
    this.cancelPending();
    this.dirty = false;
  }
}

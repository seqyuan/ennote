/**
 * The inspector lane's tab rows: one row per thing the right panel draws.
 *
 * The lane used to hold files only, so a row was `{ path, name, pane }` and the
 * id was spelled out at each call site. The generative canvas opens published
 * artifacts in the same strip, so a row becomes a tagged union and every
 * question about it — what its id is, whether a stored selection still names it,
 * how an unknown payload is read back — is answered in one place instead of by
 * string-building wherever a caller happens to need it.
 *
 * The id format is part of the storage format, not an implementation detail:
 * `activeInspectorIds` stores ids and compares them after a reload, and 0.10.0
 * and 0.11.0 wrote `file:<projectId>:<path>`. Re-spelling that string would not
 * fail loudly, it would silently unselect every restored tab — so `fileTabId` is
 * the only place the file prefix is written, and the tests pin its bytes.
 *
 * The pane a row is drawn in is defined here too, with the row: it is a
 * property of a row, and keeping it in the state module would make this file
 * import the state module that imports this one.
 */

/** Which half of the inspector lane a row lives in. */
export type InspectorPane = "main" | "side";

/** The two halves, in the order they are drawn. */
export const INSPECTOR_PANES: readonly InspectorPane[] = ["main", "side"];

/** A file the reader opened from the tree or from a message. */
export interface FileInspectorRow {
  kind: "file";
  /** Project-relative path under /workspace. */
  path: string;
  /** What the strip shows; the path's last segment when it was opened. */
  name: string;
  /** Which half of the lane it sits in. Always written, so no reader guesses. */
  pane: InspectorPane;
}

/** A published artifact opened in the same strip, e.g. a canvas widget. */
export interface ArtifactInspectorRow {
  kind: "artifact";
  artifactId: string;
  /**
   * The session the artifact was published in. Half of the row's identity, not
   * a display detail: a row is a (session, artifact) pair, so the same artifact
   * id published in another session is another row.
   */
  sessionId: string;
  name: string;
  pane: InspectorPane;
}

export type InspectorTabRow = FileInspectorRow | ArtifactInspectorRow;

/** How many inspector rows one Project keeps. localStorage is not a repository. */
export const MAX_PERSISTED_INSPECTOR_TABS = 20;

/** The id of a file row. Exported because most callers hold a path, not a row. */
export function fileTabId(projectId: string, path: string): string {
  return `file:${projectId}:${path}`;
}

/** The file a `file:` id names. The inverse of {@link fileTabId}. */
export function pathOf(tabId: string): string {
  const parts = tabId.split(":");
  return parts.length < 3 ? tabId : parts.slice(2).join(":");
}

/**
 * The id of one row in one Project.
 *
 * The kind prefix is not cosmetic: it is what keeps a file path and an artifact
 * id from ever producing the same string, so a stale selection can never be
 * resolved to the wrong kind of row. The Project belongs in the id for the same
 * reason `path` rows are scoped elsewhere — the same path in two Projects is two
 * different rows, and a row restored under the wrong owner must not match.
 *
 * An artifact id carries both of its ids, session and artifact, because the row
 * is that pair. That a given artifact id happens to be unique across the Worker
 * today is the Worker's coincidence, not a guarantee this model gets to lean on:
 * the id is what storage compares, so the model is what has to hold the pair.
 */
export function inspectorTabId(projectId: string, row: InspectorTabRow): string {
  return row.kind === "artifact"
    ? `artifact:${projectId}:${row.sessionId}:${row.artifactId}`
    : fileTabId(projectId, row.path);
}

/** Whether a stored selection still names this row, by the same id rule. */
export function rowMatchesId(projectId: string, row: InspectorTabRow, tabId: string): boolean {
  return inspectorTabId(projectId, row) === tabId;
}

/**
 * Where a newly opened row should go when the caller did not say.
 *
 * The side pane exists to compare, and an artifact opened from a transcript has
 * no other way to reach it: a reader who opens a second canvas wants the first
 * one still on screen. So the free half wins, and with both taken the new row
 * lands in the side — main stays the baseline being compared against.
 * @param active - the selected row per pane.
 */
export function nextFreePane(active: Record<InspectorPane, string | null>): InspectorPane {
  return active.main === null ? "main" : "side";
}

/**
 * Read a persisted row array, keeping the newest rows and dropping junk.
 *
 * The array is written oldest-first, so trimming drops from the front: a Project
 * that has opened hundreds of rows over months must not carry all of them, and
 * the ones the reader is likely to want back are the recent ones.
 * @param value - the stored value, of unknown shape by definition.
 * @param projectId - the Project the rows belong to; it is half of every id.
 */
export function parseInspectorTabRows(value: unknown, projectId: string): InspectorTabRow[] {
  if (!Array.isArray(value)) return [];
  const rows: InspectorTabRow[] = [];
  const seen = new Set<string>();
  for (const entry of value) {
    const row = readRow(entry);
    if (row === null) continue;
    const id = inspectorTabId(projectId, row);
    if (seen.has(id)) continue;
    seen.add(id);
    rows.push(row);
  }
  return rows.slice(-MAX_PERSISTED_INSPECTOR_TABS);
}

/** One stored entry, or `null` when this build cannot make a row out of it. */
function readRow(entry: unknown): InspectorTabRow | null {
  if (typeof entry !== "object" || entry === null) return null;
  const row = entry as Record<string, unknown>;
  const pane: InspectorPane = row.pane === "side" ? "side" : "main";
  const name = isNonEmptyString(row.name) ? row.name : null;

  if (row.kind === "artifact") {
    // An artifact row is identified by its two ids and by nothing else. Without
    // both of them there is no id to store and nothing to open, so the row is
    // dropped rather than repaired — and it must not fall through to the file
    // branch below: an artifact row carries no path, so "treat it as the old
    // shape" could only invent an empty tab that clicks into nothing.
    if (!isNonEmptyString(row.artifactId) || !isNonEmptyString(row.sessionId)) return null;
    // An unnamed artifact keeps an empty label. The surface that draws the row
    // owns the localized fallback; a hard-coded English word here would be
    // written to storage and read back as if it were the artifact's name.
    return { kind: "artifact", artifactId: row.artifactId, sessionId: row.sessionId, name: name ?? "", pane };
  }

  // Everything else is read as the old shape. 0.10.0 and 0.11.0 wrote
  // `{ path, name, pane? }` with no `kind` field at all, and those payloads are
  // still on users' disks: "no kind means it is not mine" would throw away the
  // files a reader had open on their last visit. The same reading covers a kind
  // this build does not know — a row that carries a path is a file row, and one
  // that does not is not a row at all.
  if (!isNonEmptyString(row.path)) return null;
  return { kind: "file", path: row.path, name: name ?? lastSegment(row.path), pane };
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function lastSegment(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

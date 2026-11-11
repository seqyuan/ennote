"use client";

import { Copy, Maximize2, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { FolderIcon, getFileIcon } from "./FileIcons";
import { listProjectFiles, type WorkspaceFileEntry } from "@/lib/project-files";

interface Props {
  projectId: string | null;
  displayPath?: string | null;
  onOpenFile: (filePath: string, fileName: string) => void;
  onPreviewFile: (filePath: string, fileName: string) => void;
}

/** How long a filter waits before it starts listing directories. */
const FILTER_DEBOUNCE_MS = 250;

/** How many directories one filter pass may start listing. */
const MAX_FILTER_LOADS = 12;

/** One drawn row: what it is, how deep it sits, and what it hangs under. */
interface TreeRow {
  entry: WorkspaceFileEntry;
  depth: number;
  parentPath: string | null;
}

/**
 * The workspace tree.
 *
 * The tree is derived, not recursive: one depth-first walk of the loaded entries
 * produces the rows in the order they are drawn, which is also the order the
 * arrow keys move through. That keeps expansion, filtering, and keyboard
 * navigation reading the same list instead of three separate traversals that can
 * disagree.
 *
 * Only loaded directories are walked. A filter therefore matches what has been
 * fetched so far — nothing here pretends to search the disk.
 */
export function FileTreePanel({ projectId, displayPath, onOpenFile, onPreviewFile }: Props) {
  const [entries, setEntries] = useState<WorkspaceFileEntry[]>([]);
  const [rootLoading, setRootLoading] = useState(Boolean(projectId));
  const [error, setError] = useState<string | null>(null);
  const [dirErrors, setDirErrors] = useState<Map<string, string>>(new Map());
  const [searchQuery, setSearchQuery] = useState("");
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set());
  const [dirContents, setDirContents] = useState<Map<string, WorkspaceFileEntry[]>>(new Map());
  const [focusedPath, setFocusedPath] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ path: string; x: number; y: number } | null>(null);
  const nestedControllers = useRef<Map<string, AbortController>>(new Map());
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!projectId) return;
    const controller = new AbortController();
    const controllers = nestedControllers.current;
    void listProjectFiles(projectId, "/workspace", controller.signal)
      .then((items) => {
        // A malformed/empty payload must never take down the file tree via
        // .filter on null; treat it as an empty listing.
        setEntries(items ?? []);
        setError(null);
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError((reason as Error).message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setRootLoading(false);
      });
    return () => {
      controller.abort();
      controllers.forEach((active) => active.abort());
      controllers.clear();
    };
  }, [projectId]);

  const loadDir = useCallback(async (dirPath: string) => {
    // In flight already: one listing per directory, whoever asked first.
    if (!projectId || dirContents.has(dirPath) || nestedControllers.current.has(dirPath)) return;
    const controller = new AbortController();
    nestedControllers.current.set(dirPath, controller);
    setLoadingDirs((previous) => new Set(previous).add(dirPath));
    try {
      const children = await listProjectFiles(projectId, dirPath, controller.signal);
      setDirContents((previous) => new Map(previous).set(dirPath, children));
      setDirErrors((previous) => {
        if (!previous.has(dirPath)) return previous;
        const next = new Map(previous);
        next.delete(dirPath);
        return next;
      });
    } catch (reason) {
      if (!controller.signal.aborted) {
        // The failure belongs to this directory, not to the tree: a banner would
        // claim the workspace is broken when one listing is.
        setDirErrors((previous) => new Map(previous).set(dirPath, (reason as Error).message));
        // A directory that could not be listed must not stay drawn open and
        // empty: collapse it so the row still says what it is.
        setExpandedDirs((previous) => {
          if (!previous.has(dirPath)) return previous;
          const next = new Set(previous);
          next.delete(dirPath);
          return next;
        });
      }
    } finally {
      if (nestedControllers.current.get(dirPath) === controller) {
        nestedControllers.current.delete(dirPath);
      }
      setLoadingDirs((previous) => {
        const next = new Set(previous);
        next.delete(dirPath);
        return next;
      });
    }
  }, [dirContents, projectId]);

  const collapse = useCallback((dirPath: string) => {
    setExpandedDirs((previous) => {
      if (!previous.has(dirPath)) return previous;
      const next = new Set(previous);
      next.delete(dirPath);
      return next;
    });
  }, []);

  const expand = useCallback((dirPath: string) => {
    setExpandedDirs((previous) => previous.has(dirPath) ? previous : new Set(previous).add(dirPath));
    void loadDir(dirPath);
  }, [loadDir]);

  const query = searchQuery.trim().toLowerCase();

  const rows = useMemo<TreeRow[]>(() => {
    const drawn: TreeRow[] = [];

    // While filtering, a directory is drawn open if anything under it (at any
    // loaded depth) matches, so the hit is reachable without expanding by hand.
    // Answers are cached: the walk asks about the same subtree once per ancestor.
    const answers = new Map<string, boolean>();
    const matches = (entry: WorkspaceFileEntry): boolean => {
      if (!query) return true;
      const cached = answers.get(entry.path);
      if (cached !== undefined) return cached;
      const verdict = entry.name.toLowerCase().includes(query)
        || (entry.isDir && (dirContents.get(entry.path) ?? []).some(matches));
      answers.set(entry.path, verdict);
      return verdict;
    };

    const walk = (items: WorkspaceFileEntry[], depth: number, parentPath: string | null) => {
      for (const entry of items) {
        if (!matches(entry)) continue;
        drawn.push({ entry, depth, parentPath });
        const open = entry.isDir && !dirErrors.has(entry.path)
          && (query ? true : expandedDirs.has(entry.path));
        if (!entry.isDir || !open) continue;
        const children = dirContents.get(entry.path);
        if (children) walk(children, depth + 1, entry.path);
      }
    };

    walk(entries, 0, null);
    return drawn;
  }, [entries, dirContents, expandedDirs, dirErrors, query]);

  // A filter has to see into directories it has not opened, so while one is typed
  // every directory the tree knows about is listed. It converges: each listing
  // reveals more names, the effect runs again, and it stops when nothing is left
  // unlisted. The walk above only ever walks what is loaded, so this is what
  // makes "filter matches nested files" true. Deferred a tick so the filtered
  // first paint is not blocked by the fetches it starts.
  useEffect(() => {
    if (!query) return;
    const timer = window.setTimeout(() => {
      const known = new Set<string>();
      for (const entry of entries) if (entry.isDir) known.add(entry.path);
      for (const children of dirContents.values()) {
        for (const entry of children) if (entry.isDir) known.add(entry.path);
      }
      // Bounded: a filter is typed a character at a time, and each listing costs
      // a directory walk on the Worker. Whatever does not fit trickles in on the
      // next pass, which the growing `dirContents` schedules by itself.
      let started = 0;
      for (const path of known) {
        if (started >= MAX_FILTER_LOADS) break;
        if (dirContents.has(path) || nestedControllers.current.has(path)) continue;
        void loadDir(path);
        started += 1;
      }
    }, FILTER_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [query, entries, dirContents, loadDir]);

  const focusRow = useCallback((path: string) => {
    setFocusedPath(path);
    const list = listRef.current;
    if (!list) return;
    const target = list.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`);
    target?.focus();
  }, []);

  const moveFocus = useCallback((from: number, delta: number) => {
    const next = rows[from + delta];
    if (next) focusRow(next.entry.path);
  }, [rows, focusRow]);

  const onKeyDown = useCallback((event: React.KeyboardEvent<HTMLDivElement>, index: number) => {
    // The preview button inside the row is its own control: its keys stay its own.
    if (event.target !== event.currentTarget) return;
    const row = rows[index];
    if (!row) return;
    switch (event.key) {
      case "ArrowDown": event.preventDefault(); moveFocus(index, 1); break;
      case "ArrowUp": event.preventDefault(); moveFocus(index, -1); break;
      case "Home": event.preventDefault(); if (rows[0]) focusRow(rows[0].entry.path); break;
      case "End": event.preventDefault(); if (rows.length > 0) focusRow(rows[rows.length - 1].entry.path); break;
      case "ArrowRight":
        event.preventDefault();
        if (!row.entry.isDir || query) break;
        if (!expandedDirs.has(row.entry.path)) { expand(row.entry.path); break; }
        // Expanded but still listing: stepping in now would skip the subtree.
        if (!dirContents.has(row.entry.path)) break;
        moveFocus(index, 1);
        break;
      case "ArrowLeft": {
        event.preventDefault();
        if (!query && row.entry.isDir && expandedDirs.has(row.entry.path)) { collapse(row.entry.path); break; }
        const parent = rows.findIndex((candidate) => candidate.entry.path === row.parentPath);
        if (parent >= 0) focusRow(rows[parent].entry.path);
        break;
      }
      case "Enter":
      case " ":
        event.preventDefault();
        if (row.entry.isDir) {
          if (query) break;
          if (expandedDirs.has(row.entry.path)) collapse(row.entry.path);
          else expand(row.entry.path);
        } else {
          onOpenFile(row.entry.path, row.entry.name);
        }
        break;
      case "ContextMenu":
        event.preventDefault();
        setMenu({ path: row.entry.path, ...anchorOf(event.currentTarget) });
        break;
      default:
        break;
    }
  }, [rows, expandedDirs, dirContents, query, collapse, expand, moveFocus, focusRow, onOpenFile]);

  const focusedIndex = focusedPath === null
    ? -1
    : rows.findIndex((row) => row.entry.path === focusedPath);
  // The row that had the focus is gone (a filter narrowed under it, a directory
  // collapsed). Put the focus somewhere real rather than leaving it on body.
  const staleFocus = focusedPath !== null && focusedIndex === -1;
  useEffect(() => {
    if (!staleFocus) return;
    const timer = window.setTimeout(() => {
      const held = listRef.current?.contains(document.activeElement) ?? false;
      const first = rows[0]?.entry.path;
      setFocusedPath(first ?? null);
      if (held && first) focusRow(first);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [staleFocus, rows, focusRow]);
  const activeIndex = focusedIndex >= 0 ? focusedIndex : 0;

  return (
    <div className="file-tree-panel">
      <div className="file-tree-header">
        <div className="file-tree-heading">Files</div>
        <div className="file-tree-path" title={displayPath ?? "/workspace"}>
          {projectId ? displayPath ?? "/workspace" : "No project selected"}
        </div>
        <label className="file-filter">
          <Search size={12} aria-hidden="true" />
          <span className="sr-only">Filter files</span>
          <input
            value={searchQuery}
            onChange={(event) => setSearchQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "ArrowDown" || rows.length === 0) return;
              event.preventDefault();
              focusRow(rows[0].entry.path);
            }}
            placeholder="Filter files..."
            disabled={!projectId}
          />
        </label>
      </div>

      <div className="file-tree-list" ref={listRef} onScroll={() => setMenu(null)}>
        {!projectId ? (
          <div className="file-tree-empty">Select a project to browse files.</div>
        ) : rootLoading ? (
          <div className="file-tree-state">Loading...</div>
        ) : error && entries.length === 0 ? (
          <div className="file-tree-state is-error">{error}</div>
        ) : rows.length === 0 ? (
          <div className="file-tree-state">{query ? "No files match." : "No files found."}</div>
        ) : (
          <div role="tree" aria-label="Workspace files">
            {error && <p className="file-tree-inline-error" role="alert">{error}</p>}
            {rows.map((row, index) => {
              const { entry, depth } = row;
              const isExpanded = query ? true : expandedDirs.has(entry.path);
              const loading = loadingDirs.has(entry.path);
              return (
                <div
                  key={entry.path}
                  className="file-tree-row"
                  style={{ paddingLeft: 8 + depth * 16 }}
                  role="treeitem"
                  aria-level={depth + 1}
                  aria-expanded={entry.isDir ? isExpanded : undefined}
                  aria-selected={focusedPath === entry.path || undefined}
                  tabIndex={index === activeIndex ? 0 : -1}
                  data-path={entry.path}
                  onClick={() => {
                    setFocusedPath(entry.path);
                    if (entry.isDir) {
                      // While filtering the walk decides what is open, so a
                      // toggle here would change nothing the reader can see.
                      if (query) return;
                      if (isExpanded) collapse(entry.path);
                      else expand(entry.path);
                    } else {
                      onOpenFile(entry.path, entry.name);
                    }
                  }}
                  onContextMenu={(event) => {
                    event.preventDefault();
                    focusRow(entry.path);
                    setMenu({ path: entry.path, x: event.clientX, y: event.clientY });
                  }}
                  onKeyDown={(event) => onKeyDown(event, index)}
                >
                  <span className="file-tree-main" title={entry.path}>
                    <span className="file-tree-icon">
                      {entry.isDir ? <FolderIcon size={14} open={isExpanded} /> : getFileIcon(entry.name, 14)}
                    </span>
                    <span className="file-tree-name">{entry.name}</span>
                    {!entry.isDir && <span className="file-tree-size">{formatFileSize(entry.size)}</span>}
                    {loading && <span className="file-tree-loading">...</span>}
                    {dirErrors.has(entry.path) && (
                      <span
                        className="file-tree-error-mark"
                        title={dirErrors.get(entry.path)}
                        aria-label={`Could not list ${entry.name}: ${dirErrors.get(entry.path)}`}
                      >!</span>
                    )}
                  </span>
                  {!entry.isDir && (
                    <button
                      type="button"
                      className="file-preview-action"
                      onClick={(event) => { event.stopPropagation(); onPreviewFile(entry.path, entry.name); }}
                      aria-label={`Preview ${entry.name} in a floating window`}
                      title="Open floating preview"
                      onKeyDown={(event) => {
                        // Enter/Space belong to the button; the arrows still walk
                        // the tree, so the keyboard is never stuck inside a row.
                        if (event.key === "Enter" || event.key === " ") event.stopPropagation();
                      }}
                    >
                      <Maximize2 size={12} aria-hidden="true" />
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      {menu && <RowMenu
        row={rows.find((candidate) => candidate.entry.path === menu.path)}
        at={menu}
        onClose={() => {
          const path = menu.path;
          setMenu(null);
          focusRow(path);
        }}
        onOpenFile={onOpenFile}
        onPreviewFile={onPreviewFile}
      />}
    </div>
  );
}

/**
 * The actions a row offers. All of them work on the path alone — the tree reads
 * files, it never writes them — so the menu is the same for files and folders
 * except for the actions that need a file.
 */
function RowMenu(props: {
  row: TreeRow | undefined;
  at: { path: string; x: number; y: number };
  onClose: () => void;
  onOpenFile: (path: string, name: string) => void;
  onPreviewFile: (path: string, name: string) => void;
}) {
  const { row, at, onClose, onOpenFile, onPreviewFile } = props;
  const menuRef = useRef<HTMLDivElement>(null);
  const path = row?.entry.path ?? null;

  // Keyed by path, not by the row object: every listing that lands recomputes
  // `rows`, and a focus effect on the object would keep yanking the reader back
  // to the first item while they are choosing.
  useEffect(() => {
    if (path === null) return;
    const frame = window.requestAnimationFrame(() => {
      menuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [path]);

  if (!row) return null;
  const { entry } = row;

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(entry.path);
    } catch { /* clipboard unavailable: the menu still closes */ }
    onClose();
  };

  return (
    <>
      <div className="file-tree-menu-backdrop" onPointerDown={onClose} aria-hidden="true" />
      <div
        ref={menuRef}
        className="file-tree-menu"
        role="menu"
        aria-label={`Actions for ${entry.name}`}
        style={{ left: at.x, top: at.y }}
        onKeyDown={(event) => {
          const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? []);
          const current = items.indexOf(document.activeElement as HTMLButtonElement);
          if (event.key === "Escape") { event.preventDefault(); onClose(); return; }
          if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
          event.preventDefault();
          const delta = event.key === "ArrowDown" ? 1 : -1;
          items[(current + delta + items.length) % items.length]?.focus();
        }}
      >
        {!entry.isDir && (
          <>
            <button type="button" role="menuitem" onClick={() => { onOpenFile(entry.path, entry.name); onClose(); }}>
              Open
            </button>
            <button type="button" role="menuitem" onClick={() => { onPreviewFile(entry.path, entry.name); onClose(); }}>
              Open in floating window
            </button>
          </>
        )}
        <button type="button" role="menuitem" onClick={() => void copyPath()}>
          <Copy size={12} aria-hidden="true" /> Copy path
        </button>
      </div>
    </>
  );
}

/** Where to hang a menu that was opened from the keyboard: the row's own corner. */
function anchorOf(element: Element): { x: number; y: number } {
  const box = element.getBoundingClientRect();
  return { x: box.left + 12, y: box.bottom };
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} K`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} M`;
}

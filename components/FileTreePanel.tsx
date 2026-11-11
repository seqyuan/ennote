"use client";

import { FilePlus, FolderPlus, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { FileTreeEditor, FileTreeMenu, FileTreeRow, rowAnchor } from "./FileTreeView";
import { useProjectTreeWrites } from "@/hooks/useProjectTreeWrites";
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

/**
 * How many listings one typed filter may cost in total. A filter is not a
 * workspace crawl: past this, the reader is told what has been searched rather
 * than the panel quietly listing every directory on the disk.
 */
const MAX_FILTER_LISTINGS = 36;

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
  const filterBudget = useRef(MAX_FILTER_LISTINGS);
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
    if (!query) { filterBudget.current = MAX_FILTER_LISTINGS; return; }
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
        if (started >= MAX_FILTER_LOADS || filterBudget.current <= 0) break;
        // A directory that failed is not asked again by the scan: the reader can
        // retry it by expanding it, which is a deliberate act.
        if (dirContents.has(path) || dirErrors.has(path) || nestedControllers.current.has(path)) continue;
        void loadDir(path);
        filterBudget.current -= 1;
        started += 1;
      }
    }, FILTER_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [query, entries, dirContents, dirErrors, loadDir]);

  /** Re-read one directory the panel already has open (or the root). */
  const refreshDir = useCallback(async (dirPath: string) => {
    if (!projectId) return;
    try {
      const children = await listProjectFiles(projectId, dirPath);
      if (dirPath === "/workspace") {
        setEntries(children ?? []);
      } else {
        setDirContents((previous) => new Map(previous).set(dirPath, children ?? []));
      }
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [projectId]);

  /** Drop a subtree's cached listing and its expansion, in that order. */
  const forgetDir = useCallback((dirPath: string) => {
    setDirContents((previous) => {
      const next = new Map(previous);
      for (const key of [...next.keys()]) {
        if (key === dirPath || key.startsWith(`${dirPath}/`)) next.delete(key);
      }
      return next;
    });
    setExpandedDirs((previous) => {
      const next = new Set([...previous].filter((key) => key !== dirPath && !key.startsWith(`${dirPath}/`)));
      return next.size === previous.size ? previous : next;
    });
  }, []);

  const writes = useProjectTreeWrites({
    projectId,
    rows,
    focusedPath,
    menuPath: menu?.path ?? null,
    expand,
    focusRow: (path) => focusRow(path),
    refreshDir,
    forgetDir,
    setError,
  });
  const { pending, setPending, beginCreate, beginRename, createDepth, submitCreate, submitRename, requestDelete } = writes;

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
    // The preview button inside the row is its own control for Enter and Space.
    // The arrows still walk the tree from there, so the keyboard never gets stuck
    // on a row's inner button.
    if (event.target !== event.currentTarget && (event.key === "Enter" || event.key === " ")) return;
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
        setMenu({ path: row.entry.path, ...rowAnchor(event.currentTarget) });
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
        <div className="file-tree-actions">
          <button
            type="button"
            className="file-tree-action"
            aria-label="New file"
            title="New file"
            disabled={!projectId}
            onClick={() => beginCreate("file")}
          >
            <FilePlus size={13} aria-hidden="true" />
          </button>
          <button
            type="button"
            className="file-tree-action"
            aria-label="New folder"
            title="New folder"
            disabled={!projectId}
            onClick={() => beginCreate("directory")}
          >
            <FolderPlus size={13} aria-hidden="true" />
          </button>
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
          <>
            {error && <p className="file-tree-inline-error" role="alert">{error}</p>}
            <div className="file-tree-state">{query ? "No files match." : "No files found."}</div>
          </>
        ) : (
          <>
            {error && <p className="file-tree-inline-error" role="alert">{error}</p>}
            <div role="tree" aria-label="Workspace files">
              {rows.map((row, index) => {
                const { entry, depth } = row;
                // A directory is only drawn as expanded if it really shows children:
                // while filtering, claiming otherwise would announce a subtree that
                // is not there.
                const isExpanded = query
                  ? (dirContents.get(entry.path)?.length ?? 0) > 0
                  : expandedDirs.has(entry.path);
                if (pending && pending.kind === "rename" && pending.path === entry.path) {
                  return (
                    <FileTreeEditor
                      key="editor"
                      depth={depth}
                      label={`Rename ${entry.name}`}
                      initialValue={entry.name}
                      onCommit={(value) => void submitRename(entry, value)}
                      onCancel={() => setPending(null)}
                    />
                  );
                }
                return (
                  <FileTreeRow
                    key={entry.path}
                    entry={entry}
                    depth={depth}
                    expanded={isExpanded}
                    loading={loadingDirs.has(entry.path)}
                    errorText={dirErrors.get(entry.path)}
                    tabbable={index === activeIndex}
                    selected={focusedPath === entry.path}
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
                    onPreview={() => onPreviewFile(entry.path, entry.name)}
                  />
                );
              })}
              {pending && pending.kind === "create" && (
                <FileTreeEditor
                  key="creator"
                  depth={createDepth()}
                  label={pending.entryKind === "directory" ? "New folder name" : "New file name"}
                  initialValue=""
                  onCommit={(value) => void submitCreate(value)}
                  onCancel={() => setPending(null)}
                />
              )}
            </div>
          </>
        )}
      </div>

      {menu && (
        <FileTreeMenu
          entry={rows.find((candidate) => candidate.entry.path === menu.path)?.entry}
          at={menu}
          onClose={() => {
            const path = menu.path;
            setMenu(null);
            focusRow(path);
          }}
          onOpen={() => {
            const target = rows.find((candidate) => candidate.entry.path === menu.path)?.entry;
            if (target && !target.isDir) onOpenFile(target.path, target.name);
          }}
          onPreview={() => {
            const target = rows.find((candidate) => candidate.entry.path === menu.path)?.entry;
            if (target && !target.isDir) onPreviewFile(target.path, target.name);
          }}
          onNewFile={() => beginCreate("file")}
          onNewFolder={() => beginCreate("directory")}
          onRename={() => beginRename()}
          onDelete={() => void requestDelete()}
          onError={(message) => setError(message)}
        />
      )}
    </div>
  );
}

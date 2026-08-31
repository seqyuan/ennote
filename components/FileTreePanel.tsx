"use client";

import { Maximize2, Search } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { FolderIcon, getFileIcon } from "./FileIcons";
import { listProjectFiles, type WorkspaceFileEntry } from "@/lib/project-files";

interface Props {
  projectId: string | null;
  displayPath?: string | null;
  onOpenFile: (filePath: string, fileName: string) => void;
  onPreviewFile: (filePath: string, fileName: string) => void;
}

export function FileTreePanel({ projectId, displayPath, onOpenFile, onPreviewFile }: Props) {
  const [entries, setEntries] = useState<WorkspaceFileEntry[]>([]);
  const [rootLoading, setRootLoading] = useState(Boolean(projectId));
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState("");
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set());
  const [dirContents, setDirContents] = useState<Map<string, WorkspaceFileEntry[]>>(new Map());
  const nestedControllers = useRef<Map<string, AbortController>>(new Map());

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

  const toggleDir = useCallback(async (dirPath: string) => {
    if (!projectId) return;
    if (expandedDirs.has(dirPath)) {
      setExpandedDirs((previous) => {
        const next = new Set(previous);
        next.delete(dirPath);
        return next;
      });
      return;
    }

    setExpandedDirs((previous) => new Set(previous).add(dirPath));
    if (dirContents.has(dirPath)) return;

    const controller = new AbortController();
    nestedControllers.current.set(dirPath, controller);
    setLoadingDirs((previous) => new Set(previous).add(dirPath));
    try {
      const children = await listProjectFiles(projectId, dirPath, controller.signal);
      setDirContents((previous) => new Map(previous).set(dirPath, children));
      setError(null);
    } catch (reason) {
      if (!controller.signal.aborted) setError((reason as Error).message);
    } finally {
      nestedControllers.current.delete(dirPath);
      setLoadingDirs((previous) => {
        const next = new Set(previous);
        next.delete(dirPath);
        return next;
      });
    }
  }, [dirContents, expandedDirs, projectId]);

  const normalizedQuery = searchQuery.trim().toLowerCase();
  const filtered = entries.filter((entry) => !normalizedQuery || entry.name.toLowerCase().includes(normalizedQuery));

  const renderEntries = (items: WorkspaceFileEntry[], depth: number): React.ReactNode => items.map((entry) => {
    const isExpanded = expandedDirs.has(entry.path);
    const children = dirContents.get(entry.path);
    const loading = loadingDirs.has(entry.path);

    return (
      <div key={entry.path}>
        <div className="file-tree-row" style={{ paddingLeft: 8 + depth * 16 }}>
          <button
            type="button"
            className="file-tree-main"
            onClick={() => entry.isDir ? void toggleDir(entry.path) : onOpenFile(entry.path, entry.name)}
            title={entry.path}
            aria-expanded={entry.isDir ? isExpanded : undefined}
          >
            <span className="file-tree-icon">
              {entry.isDir ? <FolderIcon size={14} open={isExpanded} /> : getFileIcon(entry.name, 14)}
            </span>
            <span className="file-tree-name">{entry.name}</span>
            {!entry.isDir && <span className="file-tree-size">{formatFileSize(entry.size)}</span>}
            {loading && <span className="file-tree-loading">...</span>}
          </button>
          {!entry.isDir && (
            <button
              type="button"
              className="file-preview-action"
              onClick={() => onPreviewFile(entry.path, entry.name)}
              aria-label={`Preview ${entry.name} in a floating window`}
              title="Open floating preview"
            >
              <Maximize2 size={12} aria-hidden="true" />
            </button>
          )}
        </div>
        {entry.isDir && isExpanded && children && renderEntries(children, depth + 1)}
      </div>
    );
  });

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
            placeholder="Filter files..."
            disabled={!projectId}
          />
        </label>
      </div>

      <div className="file-tree-list">
        {!projectId ? (
          <div className="file-tree-empty">Select a project to browse files.</div>
        ) : rootLoading ? (
          <div className="file-tree-state">Loading...</div>
        ) : error && entries.length === 0 ? (
          <div className="file-tree-state is-error">{error}</div>
        ) : filtered.length === 0 ? (
          <div className="file-tree-state">{normalizedQuery ? "No files match." : "No files found."}</div>
        ) : (
          <>
            {error && <div className="file-tree-inline-error" role="alert">{error}</div>}
            {renderEntries(filtered, 0)}
          </>
        )}
      </div>
    </div>
  );
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} K`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} M`;
}

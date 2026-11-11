"use client";

import { useEffect, useRef } from "react";
import { Copy, Maximize2, Trash2, Pencil, FilePlus, FolderPlus } from "lucide-react";

import { FolderIcon, getFileIcon } from "./FileIcons";
import type { WorkspaceFileEntry } from "@/lib/project-files";

/**
 * The tree's presentation: one row, one inline editor, one menu.
 *
 * The panel next door owns what is in the tree and what a key does; these
 * components own only what it looks like and what a row's own controls are. That
 * split is what keeps the row's DOM contract stable — the e2e selectors, the
 * roles and the aria wiring all live here.
 */

/** One row: the whole row is the treeitem, the preview action is its own button. */
export function FileTreeRow(props: {
  entry: WorkspaceFileEntry;
  depth: number;
  expanded: boolean;
  loading: boolean;
  /** Why this directory could not be listed, when it could not. */
  errorText?: string;
  /** Whether this row currently holds the roving tabindex. */
  tabbable: boolean;
  selected: boolean;
  onClick: () => void;
  onContextMenu: (event: React.MouseEvent<HTMLDivElement>) => void;
  onKeyDown: (event: React.KeyboardEvent<HTMLDivElement>) => void;
  onPreview: () => void;
}) {
  const { entry, depth, expanded, loading, errorText, tabbable, selected } = props;
  return (
    <div
      className="file-tree-row"
      style={{ paddingLeft: 8 + depth * 16 }}
      role="treeitem"
      aria-level={depth + 1}
      aria-expanded={entry.isDir ? expanded : undefined}
      aria-selected={selected || undefined}
      tabIndex={tabbable ? 0 : -1}
      data-path={entry.path}
      onClick={props.onClick}
      onContextMenu={props.onContextMenu}
      onKeyDown={props.onKeyDown}
    >
      <span className="file-tree-main" title={entry.path}>
        <span className="file-tree-icon">
          {entry.isDir ? <FolderIcon size={14} open={expanded} /> : getFileIcon(entry.name, 14)}
        </span>
        <span className="file-tree-name">{entry.name}</span>
        {!entry.isDir && <span className="file-tree-size">{formatFileSize(entry.size)}</span>}
        {loading && <span className="file-tree-loading">...</span>}
        {errorText && (
          <span className="file-tree-error-mark" title={errorText} aria-label={`Could not list ${entry.name}: ${errorText}`}>!</span>
        )}
      </span>
      {!entry.isDir && (
        <button
          type="button"
          className="file-preview-action"
          onClick={(event) => { event.stopPropagation(); props.onPreview(); }}
          aria-label={`Preview ${entry.name} in a floating window`}
          title="Open floating preview"
          onKeyDown={(event) => {
            // Enter/Space belong to the button; the arrows still walk the tree.
            if (event.key === "Enter" || event.key === " ") event.stopPropagation();
          }}
        >
          <Maximize2 size={12} aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

/**
 * A name being typed, in the tree, where the path it belongs to is.
 *
 * Enter commits, Escape cancels, and leaving the field cancels: a name that was
 * abandoned halfway is not a name.
 */
export function FileTreeEditor(props: {
  depth: number;
  initialValue: string;
  label: string;
  onCommit: (value: string) => void;
  onCancel: () => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      const input = inputRef.current;
      if (!input) return;
      input.focus();
      input.select();
    });
    return () => window.cancelAnimationFrame(frame);
  }, []);

  return (
    <div className="file-tree-row file-tree-row-editing" style={{ paddingLeft: 8 + props.depth * 16 }}>
      <span className="file-tree-main">
        <span className="file-tree-icon" aria-hidden="true" />
        <input
          ref={inputRef}
          className="file-tree-editor"
          aria-label={props.label}
          defaultValue={props.initialValue}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              props.onCommit(event.currentTarget.value);
            } else if (event.key === "Escape") {
              event.preventDefault();
              props.onCancel();
            }
          }}
          onBlur={() => props.onCancel()}
        />
      </span>
    </div>
  );
}

/** What a row offers. Every action is a workspace operation the Worker can answer. */
export function FileTreeMenu(props: {
  entry: WorkspaceFileEntry | undefined;
  at: { x: number; y: number };
  onClose: () => void;
  onOpen: () => void;
  onPreview: () => void;
  onNewFile: () => void;
  onNewFolder: () => void;
  onRename: () => void;
  onDelete: () => void;
  onError: (message: string) => void;
}) {
  const { entry, at, onClose } = props;
  const menuRef = useRef<HTMLDivElement>(null);
  const path = entry?.path ?? null;

  // Keyed by path, not by the entry object: every listing that lands recomputes
  // the rows, and a focus effect on the object would keep yanking the reader back
  // to the first item while they are choosing.
  useEffect(() => {
    if (path === null) return;
    const frame = window.requestAnimationFrame(() => {
      menuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [path]);

  if (!entry) return null;

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(entry.path);
    } catch {
      props.onError("The clipboard is unavailable in this window.");
    }
    onClose();
  };

  const run = (action: () => void) => () => { action(); onClose(); };

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
            <button type="button" role="menuitem" onClick={run(props.onOpen)}>Open</button>
            <button type="button" role="menuitem" onClick={run(props.onPreview)}>Open in floating window</button>
          </>
        )}
        <button type="button" role="menuitem" onClick={run(props.onNewFile)}>
          <FilePlus size={12} aria-hidden="true" /> New file
        </button>
        <button type="button" role="menuitem" onClick={run(props.onNewFolder)}>
          <FolderPlus size={12} aria-hidden="true" /> New folder
        </button>
        <button type="button" role="menuitem" onClick={run(props.onRename)}>
          <Pencil size={12} aria-hidden="true" /> Rename
        </button>
        <button type="button" role="menuitem" onClick={run(props.onDelete)}>
          <Trash2 size={12} aria-hidden="true" /> Delete
        </button>
        <button type="button" role="menuitem" onClick={() => void copyPath()}>
          <Copy size={12} aria-hidden="true" /> Copy path
        </button>
      </div>
    </>
  );
}

/** Where to hang a menu opened from the keyboard: the row's own corner. */
export function rowAnchor(element: Element): { x: number; y: number } {
  const box = element.getBoundingClientRect();
  return { x: box.left + 12, y: box.bottom };
}

export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} K`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} M`;
}

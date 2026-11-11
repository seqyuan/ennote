"use client";

import { useCallback, useRef, useState } from "react";

import { confirmAction } from "@/components/ui/confirm";
import {
  createProjectEntry,
  deleteProjectEntry,
  moveProjectEntry,
  splitWorkspacePath,
  type WorkspaceFileEntry,
} from "@/lib/project-files";

/** A row the tree draws, as far as a write needs to know about it. */
interface TreeRowRef {
  entry: WorkspaceFileEntry;
  depth: number;
}

/** The name being typed, in the tree, for a create or a rename. */
export type PendingName =
  | { kind: "rename"; path: string }
  | { kind: "create"; parent: string; entryKind: "file" | "directory" };

/**
 * The tree's write flows: new file, new folder, rename, delete.
 *
 * The panel owns the data (what is loaded, what is expanded); this hook owns the
 * sentences around a write — where a new entry goes, what a name may look like,
 * what has to be confirmed, and what gets refreshed afterwards. Keeping it out of
 * the component is what lets the panel stay a view.
 */
export function useProjectTreeWrites(input: {
  projectId: string | null;
  rows: readonly TreeRowRef[];
  focusedPath: string | null;
  menuPath: string | null;
  expand: (path: string) => void;
  focusRow: (path: string) => void;
  refreshDir: (path: string) => Promise<void>;
  /** Forget a subtree's cached listing (a delete or a rename invalidates it). */
  forgetDir: (path: string) => void;
  setError: (message: string | null) => void;
}) {
  const { projectId, rows, focusedPath, menuPath, expand, focusRow, refreshDir, forgetDir, setError } = input;
  // One slot, because one name is being typed at a time.
  const [pending, setPending] = useState<PendingName | null>(null);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);

  /** A name is a path segment, never a path. */
  const nameProblem = (name: string): string | null => {
    if (name.includes("/") || name === "." || name === "..") {
      return "A name cannot contain a slash or be . or ..";
    }
    return null;
  };

  const joinPath = (parent: string, name: string): string =>
    `${parent === "/workspace" ? "/workspace" : parent}/${name}`;

  /** The directory a new entry goes into: the selected row, when it is one. */
  const createParent = useCallback((): string => {
    const row = rows.find((candidate) => candidate.entry.path === focusedPath);
    if (row?.entry.isDir) return row.entry.path;
    if (row) return splitWorkspacePath(row.entry.path).parent;
    return "/workspace";
  }, [rows, focusedPath]);

  const beginCreate = useCallback((entryKind: "file" | "directory") => {
    const parent = createParent();
    setPending({ kind: "create", parent, entryKind });
    // The editor row is drawn inside the directory that will receive it.
    if (parent !== "/workspace") expand(parent);
  }, [createParent, expand]);

  const beginRename = useCallback(() => {
    const row = rows.find((candidate) => candidate.entry.path === focusedPath);
    if (row) setPending({ kind: "rename", path: row.entry.path });
  }, [rows, focusedPath]);

  /** How deep the create row is drawn: one level below its directory. */
  const createDepth = useCallback((): number => {
    if (!pending || pending.kind !== "create") return 0;
    const row = rows.find((candidate) => candidate.entry.path === pending.parent);
    return row ? row.depth + 1 : 0;
  }, [pending, rows]);

  const submitCreate = useCallback(async (rawName: string) => {
    if (!pending || pending.kind !== "create" || !projectId) return;
    const name = rawName.trim();
    if (name === "") { setPending(null); return; }
    // A name is a path segment, never a path: "/" or ".." would escape the parent
    // in the mind of whoever typed it, and the Worker would refuse it anyway.
    const problem = nameProblem(name);
    if (problem) { setError(problem); return; }
    const target = joinPath(pending.parent, name);
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      await createProjectEntry(projectId, { path: target, kind: pending.entryKind, content: "" });
      setPending(null);
      await refreshDir(pending.parent);
      // The row exists only after the listing lands, so the focus is asked for
      // once more on the next frame rather than against a list that is not there.
      window.requestAnimationFrame(() => focusRow(target));
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }, [pending, projectId, refreshDir, focusRow, setError]);

  const submitRename = useCallback(async (entry: WorkspaceFileEntry, rawName: string) => {
    if (!projectId) return;
    const name = rawName.trim();
    if (name === "" || name === entry.name) { setPending(null); return; }
    const problem = nameProblem(name);
    if (problem) { setError(problem); return; }
    const parent = splitWorkspacePath(entry.path).parent;
    const target = joinPath(parent, name);
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      await moveProjectEntry(projectId, { from: entry.path, to: target });
      setPending(null);
      // Whatever was cached under the old path described a directory that no
      // longer has that name.
      forgetDir(entry.path);
      await refreshDir(parent);
      // The row that was renamed is the row that keeps the focus.
      window.requestAnimationFrame(() => focusRow(target));
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }, [projectId, refreshDir, forgetDir, focusRow, setError]);

  const requestDelete = useCallback(async () => {
    if (!projectId) return;
    const row = rows.find((candidate) => candidate.entry.path === menuPath);
    if (!row) return;
    const { entry } = row;
    const confirmed = await confirmAction({
      title: `Delete ${entry.name}?`,
      description: entry.isDir
        ? "The directory and everything inside it is deleted. There is no trash."
        : "The file is deleted. There is no trash.",
      confirmLabel: "Delete",
      tone: "danger",
    });
    if (!confirmed) return;
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      await deleteProjectEntry(projectId, entry.path, { recursive: entry.isDir });
      forgetDir(entry.path);
      await refreshDir(splitWorkspacePath(entry.path).parent);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }, [projectId, rows, menuPath, refreshDir, forgetDir, setError]);


  return {
    pending, setPending, busy,
    beginCreate, beginRename, createDepth, submitCreate, submitRename, requestDelete,
  };
}

"use client";

import { MoreHorizontal, Pencil, Plus, Star, Trash2, Triangle } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useT } from "@/components/LocaleProvider";
import type { Session } from "@/components/settings/types";
import { isBlankSession } from "@/lib/session-blank";
import type { SidebarProjectGroup } from "@/hooks/useSidebarProjectGroups";
import { SidebarRenameDialog } from "./SidebarRenameDialog";
import { buildSessionTree, renderSessionTree } from "./SessionTree";

/**
 * Project (workspace) folder row plus its session run, aligned to
 * deepseek-harness Rows: a 34px folder row whose leading folder glyph swaps
 * to a chevron on hover, revealing pin / new-session / overflow-menu actions.
 * The overflow menu carries Rename and Delete project; both commit through the
 * AppShell-owned callbacks and surface in the shared rename/delete dialogs.
 */
export function ProjectGroup({
  group, isCurrent, isPinned, isCollapsed, onToggleCollapsed, onTogglePin,
  query, selectedSession, switchSession, archiveSession, restoreSession,
  renameSession, renameProject, deleteProject, createSessionIn, mutatingId,
  archived, onOpenArchived, runningSessionIds,
}: {
  group: SidebarProjectGroup;
  isCurrent: boolean;
  isPinned: boolean;
  isCollapsed: boolean;
  onToggleCollapsed: () => void;
  onTogglePin: () => void;
  query: string;
  selectedSession: string | null;
  switchSession: (id: string) => void;
  archiveSession: (s: Session) => void;
  restoreSession: (s: Session) => void;
  renameSession: (s: Session, title: string) => Promise<void>;
  renameProject: (projectId: string, name: string) => Promise<void>;
  deleteProject: (projectId: string) => Promise<void>;
  createSessionIn: (projectId: string) => Promise<void>;
  mutatingId: string | null;
  archived: Session[];
  onOpenArchived: () => void;
  runningSessionIds?: Set<string>;
}) {
  const [archivedOpen, setArchivedOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const t = useT();

  useEffect(() => {
    if (!menuOpen) return;
    const close = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [menuOpen]);

  const matching = query
    ? group.sessions.filter((s) => !isBlankSession(s) && s.title.toLowerCase().includes(query))
    : [
      ...group.sessions.filter((s) => isBlankSession(s) && s.id === selectedSession),
      ...group.sessions.filter((s) => !isBlankSession(s)),
    ];

  const onRowKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onToggleCollapsed();
    }
  };

  return (
    <div className="sidebar-project-group">
      <div
        role="treeitem"
        tabIndex={0}
        aria-expanded={!isCollapsed}
        aria-selected={isCurrent}
        className={`sb-project-row${menuOpen ? " sb-menu-open" : ""}`}
        onClick={onToggleCollapsed}
        onKeyDown={onRowKeyDown}
        title={group.projectName}
      >
        <span className={`sb-row-slot sb-folder${isCurrent ? " sb-folder-active" : ""}`}>
          {isCollapsed
            ? <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5Z" /></svg>
            : <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.35" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M1.5 5A1.5 1.5 0 0 1 3 3.5h3l1.2 1.5H13A1.5 1.5 0 0 1 14.5 6.5L13.6 11A1.5 1.5 0 0 1 12.1 12.5H3A1.5 1.5 0 0 1 1.5 11V5ZM1.5 8V11A1.5 1.5 0 0 0 3 12.5h10A1.5 1.5 0 0 0 14.5 11V8" /></svg>}
        </span>
        <span className="sb-row-slot sb-chevron">
          <Triangle size={10} fill="currentColor" stroke="none" style={{ transform: isCollapsed ? "none" : "rotate(90deg)", transition: "transform 0.15s" }} aria-hidden="true" />
        </span>
        <span className="sb-project-title">{group.projectName}</span>

        <span className="sb-row-actions" ref={menuRef}>
          <button
            type="button"
            className={isPinned ? "sb-pin-star" : "sb-pin-star is-unpinned"}
            title={isPinned ? t("sidebar.unpin") : t("sidebar.pin")}
            aria-label={isPinned ? t("sidebar.unpin") : t("sidebar.pin")}
            onClick={(event) => { event.stopPropagation(); onTogglePin(); }}
          >
            <Star size={13} fill={isPinned ? "currentColor" : "none"} aria-hidden="true" />
          </button>
          <button
            type="button"
            className="sb-row-icon-btn"
            aria-label={t("sidebar.newSession")}
            title={t("sidebar.newSession")}
            onClick={(event) => { event.stopPropagation(); void createSessionIn(group.projectId); }}
          >
            <Plus size={14} aria-hidden="true" />
          </button>
          <button
            type="button"
            className="sb-row-icon-btn"
            aria-label={t("sidebar.deleteProject")}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={(event) => { event.stopPropagation(); setMenuOpen((v) => !v); }}
          >
            <MoreHorizontal size={15} aria-hidden="true" />
          </button>
          {menuOpen && (
            <div className="sb-row-menu" role="menu">
              <button type="button" role="menuitem" onClick={(event) => { event.stopPropagation(); setMenuOpen(false); setRenameOpen(true); }}>
                <Pencil size={13} aria-hidden="true" /> {t("sidebar.renameProject")}
              </button>
              <button type="button" role="menuitem" className="is-danger" onClick={(event) => { event.stopPropagation(); setMenuOpen(false); setDeleteOpen(true); }}>
                <Trash2 size={13} aria-hidden="true" /> {t("sidebar.deleteProject")}
              </button>
            </div>
          )}
        </span>
      </div>

      {!isCollapsed && (
        <div className="sb-project-children">
          {group.error && <div className="sidebar-empty" style={{ color: "var(--danger)" }}>{group.error}</div>}
          {group.loading && matching.length === 0 ? (
            <div className="sidebar-empty">{t("sidebar.loading")}</div>
          ) : matching.length === 0 ? (
            <div className="sidebar-empty" role="status">
              {query ? `${t("sidebar.noSessionsMatch")} "${query}"` : t("sidebar.noSessions")}
            </div>
          ) : (
            <ul className="session-list" aria-label={`Sessions in ${group.projectName}`}>
              {renderSessionTree(buildSessionTree(matching), selectedSession, switchSession, "active", archiveSession, restoreSession, renameSession, mutatingId, 0, runningSessionIds)}
            </ul>
          )}
          <button type="button" className="sidebar-archived-toggle" aria-expanded={archivedOpen} onClick={() => {
            const next = !archivedOpen;
            setArchivedOpen(next);
            if (next) onOpenArchived();
          }}>
            {archivedOpen ? t("sidebar.hideArchived") : t("sidebar.showArchived")}
          </button>
          {archivedOpen && archived.length > 0 && (
            <ul className="session-list" aria-label={`Archived sessions in ${group.projectName}`}>
              {renderSessionTree(buildSessionTree(archived), selectedSession, switchSession, "archived", archiveSession, restoreSession, renameSession, mutatingId, 0, runningSessionIds)}
            </ul>
          )}
          {archivedOpen && archived.length === 0 && (
            <div className="sidebar-empty">{t("sidebar.noArchived")}</div>
          )}
        </div>
      )}

      {renameOpen && (
        <SidebarRenameDialog
          title={t("sidebar.renameProject")}
          initialName={group.projectName}
          onCancel={() => setRenameOpen(false)}
          onConfirm={async (name) => { await renameProject(group.projectId, name); setRenameOpen(false); }}
        />
      )}
      {deleteOpen && (
        <ProjectDeleteDialog
          projectName={group.projectName}
          onCancel={() => setDeleteOpen(false)}
          onConfirm={async () => { await deleteProject(group.projectId); setDeleteOpen(false); }}
        />
      )}
    </div>
  );
}

function ProjectDeleteDialog({ projectName, onCancel, onConfirm }: {
  projectName: string;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const t = useT();
  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (reason) {
      setError((reason as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="sb-dialog-overlay" onPointerDown={(e) => { if (e.target === e.currentTarget && !busy) onCancel(); }}>
      <div className="sb-dialog" role="dialog" aria-modal="true" aria-label={t("sidebar.deleteProject")}>
        <h3>{t("sidebar.deleteProjectTitle").replace("{name}", projectName)}</h3>
        <p>{t("sidebar.deleteProjectBody")}</p>
        {error && <div className="sb-dialog-error" role="alert">{error}</div>}
        <div className="sb-dialog-actions">
          <button type="button" onClick={onCancel} disabled={busy}>{t("sidebar.cancel")}</button>
          <button type="button" className="sb-danger" onClick={confirm} disabled={busy}>{t("sidebar.deleteProjectConfirm")}</button>
        </div>
      </div>
    </div>
  );
}
